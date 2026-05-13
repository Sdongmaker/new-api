package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

const (
	CCSwitchBootstrapPath = "/api/bootstrap/cc-switch"

	CCSwitchBootstrapActionCreated      = "created"
	CCSwitchBootstrapActionResumed      = "resumed"
	CCSwitchBootstrapActionRestored     = "restored"
	CCSwitchBootstrapActionTokenRotated = "token_rotated"
)

type CCSwitchBootstrapHeaders struct {
	ClientID  string
	Timestamp string
	Nonce     string
	Signature string
}

type CCSwitchBootstrapRequestContext struct {
	Method    string
	Path      string
	Body      []byte
	Headers   CCSwitchBootstrapHeaders
	ClientIP  string
	UserAgent string
}

type CCSwitchBootstrapRequest struct {
	InstallID         string `json:"install_id"`
	DeviceFingerprint string `json:"device_fingerprint"`
	ClientVersion     string `json:"client_version"`
	Platform          string `json:"platform"`
	Arch              string `json:"arch"`
	BuildChannel      string `json:"build_channel"`
}

type CCSwitchBootstrapResult struct {
	Action    string                    `json:"action"`
	Provider  CCSwitchBootstrapProvider `json:"provider"`
	ExpiresAt int64                     `json:"expires_at"`
}

type CCSwitchBootstrapProvider struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	BaseURL string         `json:"base_url"`
	APIKey  string         `json:"api_key"`
	Models  map[string]any `json:"models"`
}

type CCSwitchBootstrapError struct {
	StatusCode int
	Message    string
}

func (e *CCSwitchBootstrapError) Error() string {
	return e.Message
}

type ccSwitchBootstrapConfig struct {
	Enabled                bool
	Clients                map[string]string
	ProviderBaseURL        string
	TokenName              string
	SignatureWindowSeconds int64
	IPLimitPerHour         int
	DeviceLimitPerHour     int
	ServerSalt             string
}

var (
	ccSwitchBootstrapNonceMu     sync.Mutex
	ccSwitchBootstrapNonces      = map[string]int64{}
	ccSwitchBootstrapRateLimiter common.InMemoryRateLimiter
)

func HandleCCSwitchBootstrap(ctx context.Context, reqCtx CCSwitchBootstrapRequestContext) (*CCSwitchBootstrapResult, error) {
	cfg, err := loadCCSwitchBootstrapConfig()
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, bootstrapHTTPError(http.StatusForbidden, "bootstrap disabled")
	}
	if err := verifyCCSwitchBootstrapSignature(ctx, cfg, reqCtx); err != nil {
		return nil, err
	}

	var req CCSwitchBootstrapRequest
	if err := common.Unmarshal(reqCtx.Body, &req); err != nil {
		return nil, bootstrapHTTPError(http.StatusBadRequest, "invalid request body")
	}
	if err := validateCCSwitchBootstrapRequest(req); err != nil {
		return nil, err
	}

	installHash := ccSwitchBootstrapHash(cfg.ServerSalt, req.InstallID)
	fingerprintHash := ccSwitchBootstrapHash(cfg.ServerSalt, req.DeviceFingerprint)
	if err := checkCCSwitchBootstrapRateLimit(cfg, reqCtx.ClientIP, fingerprintHash); err != nil {
		return nil, err
	}

	return resolveCCSwitchBootstrapDevice(cfg, reqCtx, req, installHash, fingerprintHash)
}

func loadCCSwitchBootstrapConfig() (*ccSwitchBootstrapConfig, error) {
	enabled := ccSwitchBootstrapEnvBool("CC_SWITCH_BOOTSTRAP_ENABLED", false)
	if !enabled {
		return &ccSwitchBootstrapConfig{Enabled: false}, nil
	}

	clients := map[string]string{}
	clientsRaw := strings.TrimSpace(ccSwitchBootstrapEnv("CC_SWITCH_BOOTSTRAP_CLIENTS"))
	if clientsRaw != "" {
		if err := common.UnmarshalJsonStr(clientsRaw, &clients); err != nil {
			return nil, bootstrapHTTPError(http.StatusUnauthorized, "invalid bootstrap clients")
		}
	}
	window := int64(ccSwitchBootstrapEnvInt("CC_SWITCH_BOOTSTRAP_SIGNATURE_WINDOW_SECONDS", 300))
	if window <= 0 {
		window = 300
	}
	tokenName := strings.TrimSpace(ccSwitchBootstrapEnv("CC_SWITCH_BOOTSTRAP_TOKEN_NAME"))
	if tokenName == "" {
		tokenName = "CC Switch"
	}
	cfg := &ccSwitchBootstrapConfig{
		Enabled:                enabled,
		Clients:                clients,
		ProviderBaseURL:        strings.TrimRight(strings.TrimSpace(ccSwitchBootstrapEnv("CC_SWITCH_BOOTSTRAP_PROVIDER_BASE_URL")), "/"),
		TokenName:              tokenName,
		SignatureWindowSeconds: window,
		IPLimitPerHour:         ccSwitchBootstrapEnvInt("CC_SWITCH_BOOTSTRAP_IP_LIMIT_PER_HOUR", 20),
		DeviceLimitPerHour:     ccSwitchBootstrapEnvInt("CC_SWITCH_BOOTSTRAP_DEVICE_LIMIT_PER_HOUR", 10),
		ServerSalt:             strings.TrimSpace(ccSwitchBootstrapEnv("CC_SWITCH_BOOTSTRAP_SERVER_SALT")),
	}
	if len(cfg.Clients) == 0 {
		return nil, bootstrapHTTPError(http.StatusUnauthorized, "bootstrap client not configured")
	}
	if cfg.ProviderBaseURL == "" {
		return nil, bootstrapHTTPError(http.StatusInternalServerError, "bootstrap provider base url not configured")
	}
	if cfg.ServerSalt == "" {
		return nil, bootstrapHTTPError(http.StatusInternalServerError, "bootstrap server salt not configured")
	}
	return cfg, nil
}

func verifyCCSwitchBootstrapSignature(ctx context.Context, cfg *ccSwitchBootstrapConfig, reqCtx CCSwitchBootstrapRequestContext) error {
	clientID := strings.TrimSpace(reqCtx.Headers.ClientID)
	secret, ok := cfg.Clients[clientID]
	if !ok || clientID == "" || strings.TrimSpace(secret) == "" {
		return bootstrapHTTPError(http.StatusUnauthorized, "invalid bootstrap client")
	}
	timestamp, err := strconv.ParseInt(strings.TrimSpace(reqCtx.Headers.Timestamp), 10, 64)
	if err != nil {
		return bootstrapHTTPError(http.StatusUnauthorized, "invalid bootstrap timestamp")
	}
	now := time.Now().Unix()
	if timestamp < now-cfg.SignatureWindowSeconds || timestamp > now+cfg.SignatureWindowSeconds {
		return bootstrapHTTPError(http.StatusUnauthorized, "bootstrap timestamp expired")
	}
	nonce := strings.TrimSpace(reqCtx.Headers.Nonce)
	if nonce == "" {
		return bootstrapHTTPError(http.StatusUnauthorized, "invalid bootstrap nonce")
	}
	signature := strings.TrimSpace(reqCtx.Headers.Signature)
	if signature == "" {
		return bootstrapHTTPError(http.StatusUnauthorized, "invalid bootstrap signature")
	}
	expected := signCCSwitchBootstrapRequest(secret, reqCtx.Method, reqCtx.Path, reqCtx.Headers.Timestamp, nonce, reqCtx.Body)
	if !constantTimeHexEqual(signature, expected) {
		return bootstrapHTTPError(http.StatusUnauthorized, "invalid bootstrap signature")
	}
	if err := consumeCCSwitchBootstrapNonce(ctx, clientID, nonce, time.Duration(cfg.SignatureWindowSeconds)*time.Second); err != nil {
		return err
	}
	return nil
}

func validateCCSwitchBootstrapRequest(req CCSwitchBootstrapRequest) error {
	fields := map[string]string{
		"install_id":         req.InstallID,
		"device_fingerprint": req.DeviceFingerprint,
		"client_version":     req.ClientVersion,
		"platform":           req.Platform,
		"arch":               req.Arch,
	}
	for name, value := range fields {
		if strings.TrimSpace(value) == "" {
			return bootstrapHTTPError(http.StatusBadRequest, "missing "+name)
		}
	}
	switch req.Platform {
	case "macos", "windows", "linux":
	default:
		return bootstrapHTTPError(http.StatusBadRequest, "invalid platform")
	}
	if len(req.InstallID) > 128 || len(req.DeviceFingerprint) > 512 ||
		len(req.ClientVersion) > 64 || len(req.Platform) > 32 || len(req.Arch) > 32 ||
		len(req.BuildChannel) > 64 {
		return bootstrapHTTPError(http.StatusBadRequest, "bootstrap field too long")
	}
	return nil
}

func resolveCCSwitchBootstrapDevice(cfg *ccSwitchBootstrapConfig, reqCtx CCSwitchBootstrapRequestContext, req CCSwitchBootstrapRequest, installHash string, fingerprintHash string) (*CCSwitchBootstrapResult, error) {
	var byInstall model.BootstrapDevice
	installErr := model.DB.Where("install_id_hash = ?", installHash).First(&byInstall).Error
	var byFingerprint model.BootstrapDevice
	fingerprintErr := model.DB.Where("device_fingerprint_hash = ?", fingerprintHash).First(&byFingerprint).Error

	installFound := installErr == nil
	fingerprintFound := fingerprintErr == nil
	if installErr != nil && !errors.Is(installErr, gorm.ErrRecordNotFound) {
		return nil, errDatabase(installErr)
	}
	if fingerprintErr != nil && !errors.Is(fingerprintErr, gorm.ErrRecordNotFound) {
		return nil, errDatabase(fingerprintErr)
	}
	if installFound && fingerprintFound && byInstall.ID != byFingerprint.ID {
		_ = markBootstrapRisk(&byInstall, "hash_conflict")
		_ = markBootstrapRisk(&byFingerprint, "hash_conflict")
		return nil, bootstrapHTTPError(http.StatusConflict, "bootstrap device conflict")
	}
	if installFound {
		if !fingerprintFound && byInstall.RiskFlags == "" {
			_ = markBootstrapRisk(&byInstall, "fingerprint_changed")
		}
		return returnExistingBootstrapDevice(cfg, &byInstall, reqCtx, req, CCSwitchBootstrapActionResumed)
	}
	if fingerprintFound {
		updates := map[string]any{
			"install_id_hash": installHash,
		}
		if err := model.DB.Model(&byFingerprint).Updates(updates).Error; err != nil {
			return nil, errDatabase(err)
		}
		byFingerprint.InstallIDHash = installHash
		return returnExistingBootstrapDevice(cfg, &byFingerprint, reqCtx, req, CCSwitchBootstrapActionRestored)
	}

	result, err := createCCSwitchBootstrapDevice(cfg, reqCtx, req, installHash, fingerprintHash)
	if err == nil {
		return result, nil
	}
	if isUniqueConstraintError(err) {
		var device model.BootstrapDevice
		if retryErr := model.DB.Where("install_id_hash = ? OR device_fingerprint_hash = ?", installHash, fingerprintHash).First(&device).Error; retryErr == nil {
			return returnExistingBootstrapDevice(cfg, &device, reqCtx, req, CCSwitchBootstrapActionResumed)
		}
	}
	return nil, errDatabase(err)
}

func createCCSwitchBootstrapDevice(cfg *ccSwitchBootstrapConfig, reqCtx CCSwitchBootstrapRequestContext, req CCSwitchBootstrapRequest, installHash string, fingerprintHash string) (*CCSwitchBootstrapResult, error) {
	now := common.GetTimestamp()
	user := &model.User{
		Username:    newCCSwitchBootstrapUsername(),
		DisplayName: "CC Switch",
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Group:       "default",
	}
	var token model.Token
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := user.InsertWithTx(tx, 0); err != nil {
			return err
		}
		key, err := common.GenerateKey()
		if err != nil {
			return err
		}
		token = model.Token{
			UserId:         user.Id,
			Name:           cfg.TokenName,
			Key:            key,
			Status:         common.TokenStatusEnabled,
			CreatedTime:    now,
			AccessedTime:   now,
			ExpiredTime:    -1,
			RemainQuota:    0,
			UnlimitedQuota: true,
			Group:          "default",
		}
		if err := tx.Create(&token).Error; err != nil {
			return err
		}
		device := model.BootstrapDevice{
			InstallIDHash:         installHash,
			DeviceFingerprintHash: fingerprintHash,
			UserID:                user.Id,
			TokenID:               token.Id,
			Status:                model.BootstrapDeviceStatusActive,
			FirstIP:               reqCtx.ClientIP,
			LastIP:                reqCtx.ClientIP,
			UserAgent:             trimForBootstrap(reqCtx.UserAgent, 255),
			ClientVersion:         trimForBootstrap(req.ClientVersion, 64),
			Platform:              trimForBootstrap(req.Platform, 32),
			Arch:                  trimForBootstrap(req.Arch, 32),
			LastSeenAt:            now,
		}
		return tx.Create(&device).Error
	})
	if err != nil {
		return nil, err
	}
	user.FinalizeOAuthUserCreation(0)
	return buildCCSwitchBootstrapResult(cfg, CCSwitchBootstrapActionCreated, &token), nil
}

func returnExistingBootstrapDevice(cfg *ccSwitchBootstrapConfig, device *model.BootstrapDevice, reqCtx CCSwitchBootstrapRequestContext, req CCSwitchBootstrapRequest, action string) (*CCSwitchBootstrapResult, error) {
	if device.Status == model.BootstrapDeviceStatusBlocked {
		return nil, bootstrapHTTPError(http.StatusForbidden, "bootstrap device blocked")
	}
	var user model.User
	if err := model.DB.First(&user, device.UserID).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errDatabase(err)
		}
		return nil, bootstrapHTTPError(http.StatusForbidden, "bootstrap user unavailable")
	}
	if user.Status != common.UserStatusEnabled {
		return nil, bootstrapHTTPError(http.StatusForbidden, "bootstrap user disabled")
	}
	token, err := loadBootstrapToken(device.TokenID)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errDatabase(err)
		}
	}
	if err != nil || token.Status != common.TokenStatusEnabled {
		rotated, rotateErr := rotateCCSwitchBootstrapToken(cfg, device, user.Id)
		if rotateErr != nil {
			return nil, rotateErr
		}
		token = rotated
		action = CCSwitchBootstrapActionTokenRotated
	}
	now := common.GetTimestamp()
	updates := map[string]any{
		"last_ip":        reqCtx.ClientIP,
		"user_agent":     trimForBootstrap(reqCtx.UserAgent, 255),
		"client_version": trimForBootstrap(req.ClientVersion, 64),
		"platform":       trimForBootstrap(req.Platform, 32),
		"arch":           trimForBootstrap(req.Arch, 32),
		"last_seen_at":   now,
	}
	if err := model.DB.Model(device).Updates(updates).Error; err != nil {
		common.SysError("cc switch bootstrap device update failed: " + err.Error())
	}
	return buildCCSwitchBootstrapResult(cfg, action, token), nil
}

func loadBootstrapToken(tokenID int) (*model.Token, error) {
	if tokenID == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	var token model.Token
	err := model.DB.First(&token, tokenID).Error
	return &token, err
}

func rotateCCSwitchBootstrapToken(cfg *ccSwitchBootstrapConfig, device *model.BootstrapDevice, userID int) (*model.Token, error) {
	key, err := common.GenerateKey()
	if err != nil {
		return nil, err
	}
	now := common.GetTimestamp()
	token := &model.Token{
		UserId:         userID,
		Name:           cfg.TokenName,
		Key:            key,
		Status:         common.TokenStatusEnabled,
		CreatedTime:    now,
		AccessedTime:   now,
		ExpiredTime:    -1,
		RemainQuota:    0,
		UnlimitedQuota: true,
		Group:          "default",
	}
	if err := model.DB.Create(token).Error; err != nil {
		return nil, errDatabase(err)
	}
	if err := model.DB.Model(device).Update("token_id", token.Id).Error; err != nil {
		return nil, errDatabase(err)
	}
	return token, nil
}

func buildCCSwitchBootstrapResult(cfg *ccSwitchBootstrapConfig, action string, token *model.Token) *CCSwitchBootstrapResult {
	return &CCSwitchBootstrapResult{
		Action: action,
		Provider: CCSwitchBootstrapProvider{
			ID:      "managed-newapi",
			Name:    "NewAPI",
			BaseURL: cfg.ProviderBaseURL,
			APIKey:  "sk-" + strings.TrimPrefix(token.Key, "sk-"),
			Models:  defaultCCSwitchBootstrapModels(),
		},
		ExpiresAt: 0,
	}
}

func defaultCCSwitchBootstrapModels() map[string]any {
	return map[string]any{
		"claude": map[string]string{
			"model":        "claude-sonnet-4-6",
			"haiku_model":  "claude-haiku-4-5-20251001",
			"sonnet_model": "claude-sonnet-4-6",
			"opus_model":   "claude-opus-4-7",
		},
		"codex": map[string]string{
			"model":            "gpt-5.4",
			"reasoning_effort": "high",
		},
		"gemini": map[string]string{
			"model": "gemini-3.1-pro",
		},
	}
}

func signCCSwitchBootstrapRequest(secret string, method string, path string, timestamp string, nonce string, body []byte) string {
	bodyHash := sha256.Sum256(body)
	signingString := fmt.Sprintf("%s\n%s\n%s\n%s\n%s", method, path, timestamp, nonce, hex.EncodeToString(bodyHash[:]))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signingString))
	return hex.EncodeToString(mac.Sum(nil))
}

func constantTimeHexEqual(actual string, expected string) bool {
	actualBytes, err := hex.DecodeString(actual)
	if err != nil {
		return false
	}
	expectedBytes, err := hex.DecodeString(expected)
	if err != nil {
		return false
	}
	return hmac.Equal(actualBytes, expectedBytes)
}

func consumeCCSwitchBootstrapNonce(ctx context.Context, clientID string, nonce string, ttl time.Duration) error {
	key := "bootstrap_nonce:" + clientID + ":" + nonce
	if common.RedisEnabled && common.RDB != nil {
		ok, err := common.RDB.SetNX(ctx, key, "1", ttl).Result()
		if err != nil {
			return errDatabase(err)
		}
		if !ok {
			return bootstrapHTTPError(http.StatusUnauthorized, "bootstrap nonce replayed")
		}
		return nil
	}
	now := time.Now().Unix()
	expiresAt := now + int64(ttl.Seconds())
	ccSwitchBootstrapNonceMu.Lock()
	defer ccSwitchBootstrapNonceMu.Unlock()
	for cachedKey, cachedExpiresAt := range ccSwitchBootstrapNonces {
		if cachedExpiresAt <= now {
			delete(ccSwitchBootstrapNonces, cachedKey)
		}
	}
	if cachedExpiresAt, ok := ccSwitchBootstrapNonces[key]; ok && cachedExpiresAt > now {
		return bootstrapHTTPError(http.StatusUnauthorized, "bootstrap nonce replayed")
	}
	ccSwitchBootstrapNonces[key] = expiresAt
	return nil
}

func checkCCSwitchBootstrapRateLimit(cfg *ccSwitchBootstrapConfig, clientIP string, fingerprintHash string) error {
	ccSwitchBootstrapRateLimiter.Init(time.Hour)
	if cfg.IPLimitPerHour > 0 && strings.TrimSpace(clientIP) != "" {
		if !ccSwitchBootstrapRateLimiter.Request("ip:"+clientIP, cfg.IPLimitPerHour, 3600) {
			return bootstrapHTTPError(http.StatusTooManyRequests, "bootstrap rate limited")
		}
	}
	if cfg.DeviceLimitPerHour > 0 {
		if !ccSwitchBootstrapRateLimiter.Request("device:"+fingerprintHash, cfg.DeviceLimitPerHour, 3600) {
			return bootstrapHTTPError(http.StatusTooManyRequests, "bootstrap rate limited")
		}
	}
	return nil
}

func ccSwitchBootstrapHash(salt string, value string) string {
	sum := sha256.Sum256([]byte(salt + value))
	return hex.EncodeToString(sum[:])
}

func ccSwitchBootstrapEnv(key string) string {
	return os.Getenv(key)
}

func ccSwitchBootstrapEnvBool(key string, defaultValue bool) bool {
	raw := strings.TrimSpace(ccSwitchBootstrapEnv(key))
	if raw == "" {
		return defaultValue
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return defaultValue
	}
	return value
}

func ccSwitchBootstrapEnvInt(key string, defaultValue int) int {
	raw := strings.TrimSpace(ccSwitchBootstrapEnv(key))
	if raw == "" {
		return defaultValue
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return defaultValue
	}
	return value
}

func bootstrapHTTPError(statusCode int, message string) error {
	return &CCSwitchBootstrapError{StatusCode: statusCode, Message: message}
}

func errDatabase(err error) error {
	if err == nil {
		return nil
	}
	common.SysError("cc switch bootstrap database error: " + err.Error())
	return &CCSwitchBootstrapError{StatusCode: http.StatusInternalServerError, Message: "bootstrap internal error"}
}

func newCCSwitchBootstrapUsername() string {
	const prefix = "ccs_"
	suffixLen := model.UserNameMaxLength - len(prefix)
	if suffixLen > 12 {
		suffixLen = 12
	}
	return prefix + common.GetRandomString(suffixLen)
}

func trimForBootstrap(value string, maxLen int) string {
	value = strings.TrimSpace(value)
	if len(value) <= maxLen {
		return value
	}
	return value[:maxLen]
}

func markBootstrapRisk(device *model.BootstrapDevice, flag string) error {
	if device == nil || flag == "" {
		return nil
	}
	next := device.RiskFlags
	if next == "" {
		next = flag
	} else if !strings.Contains(","+next+",", ","+flag+",") {
		next += "," + flag
	}
	device.RiskFlags = next
	return model.DB.Model(device).Update("risk_flags", next).Error
}

func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") ||
		strings.Contains(msg, "duplicate") ||
		strings.Contains(msg, "constraint")
}
