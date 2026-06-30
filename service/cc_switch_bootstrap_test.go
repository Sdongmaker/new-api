package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const ccSwitchBootstrapTestBody = `{"install_id":"8e8b6a40-4214-44cb-b82e-4eecf09f42e8","device_fingerprint":"v1:macos:stable-device-fingerprint","client_version":"3.14.1-proprietary.1","platform":"macos","arch":"aarch64","build_channel":"proprietary"}`

func setupCCSwitchBootstrapServiceTest(t *testing.T) *gorm.DB {
	t.Helper()

	originalDB := model.DB
	originalLOGDB := model.LOG_DB
	originalMainDatabaseType := common.MainDatabaseType()
	originalLogDatabaseType := common.LogDatabaseType()
	originalRedisEnabled := common.RedisEnabled
	originalRegisterEnabled := common.RegisterEnabled
	originalQuotaForNewUser := common.QuotaForNewUser

	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	common.RegisterEnabled = false
	common.QuotaForNewUser = 12345

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Log{}, &model.BootstrapDevice{}, &model.BootstrapClaimTicket{}))

	setCCSwitchBootstrapTestEnv(t, map[string]string{
		"CC_SWITCH_BOOTSTRAP_ENABLED":                  "true",
		"CC_SWITCH_BOOTSTRAP_CLIENTS":                  `{"cc-switch-proprietary":"test-secret"}`,
		"CC_SWITCH_BOOTSTRAP_PROVIDER_BASE_URL":        "https://api.example.com/",
		"CC_SWITCH_BOOTSTRAP_SERVER_SALT":              "test-bootstrap-salt",
		"CC_SWITCH_BOOTSTRAP_SIGNATURE_WINDOW_SECONDS": "300",
		"CC_SWITCH_BOOTSTRAP_IP_LIMIT_PER_HOUR":        "100",
		"CC_SWITCH_BOOTSTRAP_DEVICE_LIMIT_PER_HOUR":    "100",
	})
	resetCCSwitchBootstrapStoresForTest()

	t.Cleanup(func() {
		resetCCSwitchBootstrapStoresForTest()
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
		model.DB = originalDB
		model.LOG_DB = originalLOGDB
		common.SetDatabaseTypes(originalMainDatabaseType, originalLogDatabaseType)
		common.RedisEnabled = originalRedisEnabled
		common.RegisterEnabled = originalRegisterEnabled
		common.QuotaForNewUser = originalQuotaForNewUser
	})

	return db
}

func setCCSwitchBootstrapTestEnv(t *testing.T, values map[string]string) {
	t.Helper()
	for key, value := range values {
		t.Setenv(key, value)
	}
}

func resetCCSwitchBootstrapStoresForTest() {
	ccSwitchBootstrapNonceMu.Lock()
	ccSwitchBootstrapNonces = map[string]int64{}
	ccSwitchBootstrapNonceMu.Unlock()
	ccSwitchBootstrapRateLimiter.Reset()
}

func replaceCCSwitchBootstrapBodyField(body string, oldValue string, newValue string) string {
	return strings.Replace(body, oldValue, newValue, 1)
}

func signedCCSwitchBootstrapHeaders(t *testing.T, body string, nonce string) CCSwitchBootstrapHeaders {
	t.Helper()
	return signedCCSwitchBootstrapHeadersForPath(t, CCSwitchBootstrapPath, body, nonce)
}

func signedCCSwitchBootstrapHeadersForPath(t *testing.T, path string, body string, nonce string) CCSwitchBootstrapHeaders {
	t.Helper()
	timestamp := time.Now().Unix()
	bodyHash := sha256.Sum256([]byte(body))
	signingString := fmt.Sprintf("%s\n%s\n%d\n%s\n%s", http.MethodPost, path, timestamp, nonce, hex.EncodeToString(bodyHash[:]))
	mac := hmac.New(sha256.New, []byte("test-secret"))
	_, err := mac.Write([]byte(signingString))
	require.NoError(t, err)
	return CCSwitchBootstrapHeaders{
		ClientID:  "cc-switch-proprietary",
		Timestamp: fmt.Sprintf("%d", timestamp),
		Nonce:     nonce,
		Signature: hex.EncodeToString(mac.Sum(nil)),
	}
}

func TestCCSwitchBootstrapClaimLinkCreatesConsumableTicket(t *testing.T) {
	db := setupCCSwitchBootstrapServiceTest(t)

	created, err := HandleCCSwitchBootstrap(context.Background(), CCSwitchBootstrapRequestContext{
		Method:    http.MethodPost,
		Path:      CCSwitchBootstrapPath,
		Body:      []byte(ccSwitchBootstrapTestBody),
		Headers:   signedCCSwitchBootstrapHeaders(t, ccSwitchBootstrapTestBody, "nonce-claim-bootstrap"),
		ClientIP:  "203.0.113.10",
		UserAgent: "cc-switch-test",
	})
	require.NoError(t, err)
	require.NotEmpty(t, created.Provider.APIKey)

	claimBody := strings.TrimSuffix(ccSwitchBootstrapTestBody, "}") + `,"redirect_path":"/console/topup?show_history=true"}`
	result, err := HandleCCSwitchBootstrapClaimLink(context.Background(), CCSwitchBootstrapRequestContext{
		Method:    http.MethodPost,
		Path:      CCSwitchBootstrapClaimLinkPath,
		Body:      []byte(claimBody),
		Headers:   signedCCSwitchBootstrapHeadersForPath(t, CCSwitchBootstrapClaimLinkPath, claimBody, "nonce-claim-link"),
		ClientIP:  "203.0.113.10",
		UserAgent: "cc-switch-test",
	})
	require.NoError(t, err)
	require.Greater(t, result.ExpiresAt, common.GetTimestamp())

	parsed, err := url.Parse(result.ClaimURL)
	require.NoError(t, err)
	require.Empty(t, parsed.RawQuery)
	require.NotEmpty(t, parsed.Fragment)
	fragment, err := url.ParseQuery(parsed.Fragment)
	require.NoError(t, err)
	require.Equal(t, "/console/topup?show_history=true", fragment.Get("redirect"))
	ticket := fragment.Get("ticket")
	require.NotEmpty(t, ticket)

	var stored model.BootstrapClaimTicket
	require.NoError(t, db.First(&stored).Error)
	require.NotEqual(t, ticket, stored.TicketHash)
	require.Len(t, stored.TicketHash, 64)
	require.Zero(t, stored.ConsumedAt)

	claim, err := ConsumeCCSwitchBootstrapClaimTicket(ticket)
	require.NoError(t, err)
	require.Equal(t, stored.UserID, claim.User.Id)
	require.Equal(t, "/console/topup?show_history=true", claim.RedirectPath)
	require.True(t, claim.NeedsProfileSetup)

	_, err = ConsumeCCSwitchBootstrapClaimTicket(ticket)
	requireBootstrapStatus(t, err, http.StatusUnauthorized)
}

func TestCCSwitchBootstrapClaimRedirectPathAllowsOnlyTopUp(t *testing.T) {
	require.Equal(t, "/console/topup", normalizeCCSwitchClaimRedirectPath(""))
	require.Equal(t, "/console/topup", normalizeCCSwitchClaimRedirectPath("/logout"))
	require.Equal(t, "/console/topup", normalizeCCSwitchClaimRedirectPath("/api/user/logout"))
	require.Equal(t, "/console/topup", normalizeCCSwitchClaimRedirectPath("//evil.example/console/topup"))
	require.Equal(t, "/console/topup", normalizeCCSwitchClaimRedirectPath("https://evil.example/console/topup"))
	require.Equal(t, "/console/topup?show_history=true", normalizeCCSwitchClaimRedirectPath("/console/topup?show_history=true"))
}

func TestCCSwitchBootstrapClaimTicketCleanupRemovesExpiredDBTickets(t *testing.T) {
	db := setupCCSwitchBootstrapServiceTest(t)
	now := common.GetTimestamp()
	expired := model.BootstrapClaimTicket{
		TicketHash:   ccSwitchBootstrapHash("claim-ticket", "expired-cleanup-ticket"),
		UserID:       1,
		DeviceID:     1,
		RedirectPath: "/console/topup",
		ExpiresAt:    now - 1,
	}
	active := model.BootstrapClaimTicket{
		TicketHash:   ccSwitchBootstrapHash("claim-ticket", "active-cleanup-ticket"),
		UserID:       1,
		DeviceID:     1,
		RedirectPath: "/console/topup",
		ExpiresAt:    now + 60,
	}
	require.NoError(t, db.Create(&expired).Error)
	require.NoError(t, db.Create(&active).Error)

	_, _, err := createCCSwitchBootstrapClaimTicket(context.Background(), 1, 1, "/console/topup")
	require.NoError(t, err)

	var expiredCount int64
	require.NoError(t, db.Model(&model.BootstrapClaimTicket{}).Where("ticket_hash = ?", expired.TicketHash).Count(&expiredCount).Error)
	require.Zero(t, expiredCount)

	var activeCount int64
	require.NoError(t, db.Model(&model.BootstrapClaimTicket{}).Where("ticket_hash = ?", active.TicketHash).Count(&activeCount).Error)
	require.Equal(t, int64(1), activeCount)
}

func TestCCSwitchBootstrapClaimLinkRejectsUnavailableDeviceOrUser(t *testing.T) {
	db := setupCCSwitchBootstrapServiceTest(t)

	_, err := HandleCCSwitchBootstrap(context.Background(), CCSwitchBootstrapRequestContext{
		Method:    http.MethodPost,
		Path:      CCSwitchBootstrapPath,
		Body:      []byte(ccSwitchBootstrapTestBody),
		Headers:   signedCCSwitchBootstrapHeaders(t, ccSwitchBootstrapTestBody, "nonce-claim-unavailable-bootstrap"),
		ClientIP:  "203.0.113.10",
		UserAgent: "cc-switch-test",
	})
	require.NoError(t, err)

	claimBody := strings.TrimSuffix(ccSwitchBootstrapTestBody, "}") + `,"redirect_path":"/console/topup"}`
	makeRequest := func(nonce string) error {
		_, requestErr := HandleCCSwitchBootstrapClaimLink(context.Background(), CCSwitchBootstrapRequestContext{
			Method:    http.MethodPost,
			Path:      CCSwitchBootstrapClaimLinkPath,
			Body:      []byte(claimBody),
			Headers:   signedCCSwitchBootstrapHeadersForPath(t, CCSwitchBootstrapClaimLinkPath, claimBody, nonce),
			ClientIP:  "203.0.113.10",
			UserAgent: "cc-switch-test",
		})
		return requestErr
	}

	require.NoError(t, db.Model(&model.BootstrapDevice{}).Where("1 = 1").Update("status", model.BootstrapDeviceStatusBlocked).Error)
	err = makeRequest("nonce-claim-blocked")
	requireBootstrapStatus(t, err, http.StatusForbidden)

	require.NoError(t, db.Model(&model.BootstrapDevice{}).Where("1 = 1").Update("status", model.BootstrapDeviceStatusActive).Error)
	require.NoError(t, db.Model(&model.User{}).Where("1 = 1").Update("status", common.UserStatusDisabled).Error)
	err = makeRequest("nonce-claim-disabled")
	requireBootstrapStatus(t, err, http.StatusForbidden)

	require.NoError(t, db.Model(&model.User{}).Where("1 = 1").Update("status", common.UserStatusEnabled).Error)
	missingBody := replaceCCSwitchBootstrapBodyField(claimBody, "stable-device-fingerprint", "missing-device-fingerprint")
	_, err = HandleCCSwitchBootstrapClaimLink(context.Background(), CCSwitchBootstrapRequestContext{
		Method:    http.MethodPost,
		Path:      CCSwitchBootstrapClaimLinkPath,
		Body:      []byte(missingBody),
		Headers:   signedCCSwitchBootstrapHeadersForPath(t, CCSwitchBootstrapClaimLinkPath, missingBody, "nonce-claim-missing"),
		ClientIP:  "203.0.113.10",
		UserAgent: "cc-switch-test",
	})
	requireBootstrapStatus(t, err, http.StatusConflict)
}

func TestCCSwitchBootstrapClaimTicketExpires(t *testing.T) {
	db := setupCCSwitchBootstrapServiceTest(t)

	ticket := "expired-ticket"
	record := model.BootstrapClaimTicket{
		TicketHash:   ccSwitchBootstrapHash("claim-ticket", ticket),
		UserID:       1,
		DeviceID:     1,
		RedirectPath: "/console/topup",
		ExpiresAt:    common.GetTimestamp() - 1,
	}
	require.NoError(t, db.Create(&record).Error)

	_, err := ConsumeCCSwitchBootstrapClaimTicket(ticket)
	requireBootstrapStatus(t, err, http.StatusUnauthorized)
}

func TestCCSwitchBootstrapClaimTicketDetectsCompletedProfile(t *testing.T) {
	db := setupCCSwitchBootstrapServiceTest(t)

	_, err := HandleCCSwitchBootstrap(context.Background(), CCSwitchBootstrapRequestContext{
		Method:    http.MethodPost,
		Path:      CCSwitchBootstrapPath,
		Body:      []byte(ccSwitchBootstrapTestBody),
		Headers:   signedCCSwitchBootstrapHeaders(t, ccSwitchBootstrapTestBody, "nonce-claim-complete-bootstrap"),
		ClientIP:  "203.0.113.10",
		UserAgent: "cc-switch-test",
	})
	require.NoError(t, err)
	hashedPassword, err := common.Password2Hash("claimed-pass")
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.User{}).Where("1 = 1").Updates(map[string]any{
		"username": "claimed-user",
		"password": hashedPassword,
	}).Error)

	claimBody := strings.TrimSuffix(ccSwitchBootstrapTestBody, "}") + `,"redirect_path":"/console/topup"}`
	result, err := HandleCCSwitchBootstrapClaimLink(context.Background(), CCSwitchBootstrapRequestContext{
		Method:    http.MethodPost,
		Path:      CCSwitchBootstrapClaimLinkPath,
		Body:      []byte(claimBody),
		Headers:   signedCCSwitchBootstrapHeadersForPath(t, CCSwitchBootstrapClaimLinkPath, claimBody, "nonce-claim-complete-link"),
		ClientIP:  "203.0.113.10",
		UserAgent: "cc-switch-test",
	})
	require.NoError(t, err)
	parsed, err := url.Parse(result.ClaimURL)
	require.NoError(t, err)
	fragment, err := url.ParseQuery(parsed.Fragment)
	require.NoError(t, err)

	claim, err := ConsumeCCSwitchBootstrapClaimTicket(fragment.Get("ticket"))
	require.NoError(t, err)
	require.False(t, claim.NeedsProfileSetup)
	require.Empty(t, claim.User.Password)
}

func TestCCSwitchBootstrapCreatesUserTokenAndDevice(t *testing.T) {
	db := setupCCSwitchBootstrapServiceTest(t)

	result, err := HandleCCSwitchBootstrap(context.Background(), CCSwitchBootstrapRequestContext{
		Method:    http.MethodPost,
		Path:      CCSwitchBootstrapPath,
		Body:      []byte(ccSwitchBootstrapTestBody),
		Headers:   signedCCSwitchBootstrapHeaders(t, ccSwitchBootstrapTestBody, "nonce-create"),
		ClientIP:  "203.0.113.10",
		UserAgent: "cc-switch-test",
	})
	require.NoError(t, err)
	require.Equal(t, CCSwitchBootstrapActionCreated, result.Action)
	require.Equal(t, "managed-newapi", result.Provider.ID)
	require.Equal(t, "NewAPI", result.Provider.Name)
	require.Equal(t, "https://api.example.com", result.Provider.BaseURL)
	require.True(t, strings.HasPrefix(result.Provider.APIKey, "sk-"))
	require.EqualValues(t, 0, result.ExpiresAt)

	var userCount int64
	require.NoError(t, db.Model(&model.User{}).Count(&userCount).Error)
	require.EqualValues(t, 1, userCount)

	var user model.User
	require.NoError(t, db.First(&user).Error)
	require.Equal(t, common.RoleCommonUser, user.Role)
	require.Equal(t, common.UserStatusEnabled, user.Status)
	require.Equal(t, 12345, user.Quota)
	require.True(t, strings.HasPrefix(user.Username, "ccs_"))
	require.LessOrEqual(t, len(user.Username), model.UserNameMaxLength)

	var token model.Token
	require.NoError(t, db.First(&token).Error)
	require.Equal(t, user.Id, token.UserId)
	require.Equal(t, common.TokenStatusEnabled, token.Status)
	require.Equal(t, int64(-1), token.ExpiredTime)
	require.True(t, token.UnlimitedQuota)
	require.Equal(t, 0, token.RemainQuota)
	require.Equal(t, "CC Switch", token.Name)
	require.Equal(t, "sk-"+token.Key, result.Provider.APIKey)

	var device model.BootstrapDevice
	require.NoError(t, db.First(&device).Error)
	require.Equal(t, user.Id, device.UserID)
	require.Equal(t, token.Id, device.TokenID)
	require.Equal(t, model.BootstrapDeviceStatusActive, device.Status)
	require.Len(t, device.InstallIDHash, 64)
	require.Len(t, device.DeviceFingerprintHash, 64)
	require.NotContains(t, device.InstallIDHash, "8e8b6a40")
	require.NotContains(t, device.DeviceFingerprintHash, "stable-device-fingerprint")
}

func TestCCSwitchBootstrapRepeatedInstallReturnsSameToken(t *testing.T) {
	db := setupCCSwitchBootstrapServiceTest(t)

	first, err := HandleCCSwitchBootstrap(context.Background(), CCSwitchBootstrapRequestContext{
		Method:    http.MethodPost,
		Path:      CCSwitchBootstrapPath,
		Body:      []byte(ccSwitchBootstrapTestBody),
		Headers:   signedCCSwitchBootstrapHeaders(t, ccSwitchBootstrapTestBody, "nonce-repeat-1"),
		ClientIP:  "203.0.113.10",
		UserAgent: "cc-switch-test",
	})
	require.NoError(t, err)

	second, err := HandleCCSwitchBootstrap(context.Background(), CCSwitchBootstrapRequestContext{
		Method:    http.MethodPost,
		Path:      CCSwitchBootstrapPath,
		Body:      []byte(ccSwitchBootstrapTestBody),
		Headers:   signedCCSwitchBootstrapHeaders(t, ccSwitchBootstrapTestBody, "nonce-repeat-2"),
		ClientIP:  "203.0.113.10",
		UserAgent: "cc-switch-test",
	})
	require.NoError(t, err)
	require.Equal(t, CCSwitchBootstrapActionResumed, second.Action)
	require.Equal(t, first.Provider.APIKey, second.Provider.APIKey)

	var userCount int64
	var tokenCount int64
	var deviceCount int64
	require.NoError(t, db.Model(&model.User{}).Count(&userCount).Error)
	require.NoError(t, db.Model(&model.Token{}).Count(&tokenCount).Error)
	require.NoError(t, db.Model(&model.BootstrapDevice{}).Count(&deviceCount).Error)
	require.EqualValues(t, 1, userCount)
	require.EqualValues(t, 1, tokenCount)
	require.EqualValues(t, 1, deviceCount)
}

func TestCCSwitchBootstrapRestoresSameFingerprintWithNewInstallID(t *testing.T) {
	db := setupCCSwitchBootstrapServiceTest(t)

	first, err := HandleCCSwitchBootstrap(context.Background(), CCSwitchBootstrapRequestContext{
		Method:    http.MethodPost,
		Path:      CCSwitchBootstrapPath,
		Body:      []byte(ccSwitchBootstrapTestBody),
		Headers:   signedCCSwitchBootstrapHeaders(t, ccSwitchBootstrapTestBody, "nonce-restore-1"),
		ClientIP:  "203.0.113.10",
		UserAgent: "cc-switch-test",
	})
	require.NoError(t, err)

	reinstallBody := replaceCCSwitchBootstrapBodyField(ccSwitchBootstrapTestBody, "8e8b6a40-4214-44cb-b82e-4eecf09f42e8", "4f4a78f9-76ca-47fc-9c7a-3db23dfc9731")
	second, err := HandleCCSwitchBootstrap(context.Background(), CCSwitchBootstrapRequestContext{
		Method:    http.MethodPost,
		Path:      CCSwitchBootstrapPath,
		Body:      []byte(reinstallBody),
		Headers:   signedCCSwitchBootstrapHeaders(t, reinstallBody, "nonce-restore-2"),
		ClientIP:  "203.0.113.10",
		UserAgent: "cc-switch-test",
	})
	require.NoError(t, err)
	require.Equal(t, CCSwitchBootstrapActionRestored, second.Action)
	require.Equal(t, first.Provider.APIKey, second.Provider.APIKey)

	var userCount int64
	var deviceCount int64
	require.NoError(t, db.Model(&model.User{}).Count(&userCount).Error)
	require.NoError(t, db.Model(&model.BootstrapDevice{}).Count(&deviceCount).Error)
	require.EqualValues(t, 1, userCount)
	require.EqualValues(t, 1, deviceCount)
}

func TestCCSwitchBootstrapRotatesDeletedTokenWithoutIncreasingQuota(t *testing.T) {
	db := setupCCSwitchBootstrapServiceTest(t)

	first, err := HandleCCSwitchBootstrap(context.Background(), CCSwitchBootstrapRequestContext{
		Method:    http.MethodPost,
		Path:      CCSwitchBootstrapPath,
		Body:      []byte(ccSwitchBootstrapTestBody),
		Headers:   signedCCSwitchBootstrapHeaders(t, ccSwitchBootstrapTestBody, "nonce-rotate-1"),
		ClientIP:  "203.0.113.10",
		UserAgent: "cc-switch-test",
	})
	require.NoError(t, err)

	var token model.Token
	require.NoError(t, db.First(&token).Error)
	require.NoError(t, db.Delete(&token).Error)

	second, err := HandleCCSwitchBootstrap(context.Background(), CCSwitchBootstrapRequestContext{
		Method:    http.MethodPost,
		Path:      CCSwitchBootstrapPath,
		Body:      []byte(ccSwitchBootstrapTestBody),
		Headers:   signedCCSwitchBootstrapHeaders(t, ccSwitchBootstrapTestBody, "nonce-rotate-2"),
		ClientIP:  "203.0.113.10",
		UserAgent: "cc-switch-test",
	})
	require.NoError(t, err)
	require.Equal(t, CCSwitchBootstrapActionTokenRotated, second.Action)
	require.NotEqual(t, first.Provider.APIKey, second.Provider.APIKey)

	var user model.User
	require.NoError(t, db.First(&user).Error)
	require.Equal(t, 12345, user.Quota)

	var activeTokenCount int64
	require.NoError(t, db.Model(&model.Token{}).Count(&activeTokenCount).Error)
	require.EqualValues(t, 1, activeTokenCount)
}

func TestCCSwitchBootstrapRejectsDisabledUserAndBlockedDevice(t *testing.T) {
	db := setupCCSwitchBootstrapServiceTest(t)

	_, err := HandleCCSwitchBootstrap(context.Background(), CCSwitchBootstrapRequestContext{
		Method:    http.MethodPost,
		Path:      CCSwitchBootstrapPath,
		Body:      []byte(ccSwitchBootstrapTestBody),
		Headers:   signedCCSwitchBootstrapHeaders(t, ccSwitchBootstrapTestBody, "nonce-disabled-1"),
		ClientIP:  "203.0.113.10",
		UserAgent: "cc-switch-test",
	})
	require.NoError(t, err)

	require.NoError(t, db.Model(&model.User{}).Where("1 = 1").Update("status", common.UserStatusDisabled).Error)
	_, err = HandleCCSwitchBootstrap(context.Background(), CCSwitchBootstrapRequestContext{
		Method:    http.MethodPost,
		Path:      CCSwitchBootstrapPath,
		Body:      []byte(ccSwitchBootstrapTestBody),
		Headers:   signedCCSwitchBootstrapHeaders(t, ccSwitchBootstrapTestBody, "nonce-disabled-2"),
		ClientIP:  "203.0.113.10",
		UserAgent: "cc-switch-test",
	})
	requireBootstrapStatus(t, err, http.StatusForbidden)

	require.NoError(t, db.Model(&model.User{}).Where("1 = 1").Update("status", common.UserStatusEnabled).Error)
	require.NoError(t, db.Model(&model.BootstrapDevice{}).Where("1 = 1").Update("status", model.BootstrapDeviceStatusBlocked).Error)
	_, err = HandleCCSwitchBootstrap(context.Background(), CCSwitchBootstrapRequestContext{
		Method:    http.MethodPost,
		Path:      CCSwitchBootstrapPath,
		Body:      []byte(ccSwitchBootstrapTestBody),
		Headers:   signedCCSwitchBootstrapHeaders(t, ccSwitchBootstrapTestBody, "nonce-disabled-3"),
		ClientIP:  "203.0.113.10",
		UserAgent: "cc-switch-test",
	})
	requireBootstrapStatus(t, err, http.StatusForbidden)
}

func TestCCSwitchBootstrapRejectsHashConflict(t *testing.T) {
	setupCCSwitchBootstrapServiceTest(t)

	first, err := HandleCCSwitchBootstrap(context.Background(), CCSwitchBootstrapRequestContext{
		Method:    http.MethodPost,
		Path:      CCSwitchBootstrapPath,
		Body:      []byte(ccSwitchBootstrapTestBody),
		Headers:   signedCCSwitchBootstrapHeaders(t, ccSwitchBootstrapTestBody, "nonce-conflict-1"),
		ClientIP:  "203.0.113.10",
		UserAgent: "cc-switch-test",
	})
	require.NoError(t, err)
	require.NotEmpty(t, first.Provider.APIKey)

	secondBody := replaceCCSwitchBootstrapBodyField(ccSwitchBootstrapTestBody, "8e8b6a40-4214-44cb-b82e-4eecf09f42e8", "4f4a78f9-76ca-47fc-9c7a-3db23dfc9731")
	secondBody = replaceCCSwitchBootstrapBodyField(secondBody, "stable-device-fingerprint", "other-device-fingerprint")
	_, err = HandleCCSwitchBootstrap(context.Background(), CCSwitchBootstrapRequestContext{
		Method:    http.MethodPost,
		Path:      CCSwitchBootstrapPath,
		Body:      []byte(secondBody),
		Headers:   signedCCSwitchBootstrapHeaders(t, secondBody, "nonce-conflict-2"),
		ClientIP:  "203.0.113.11",
		UserAgent: "cc-switch-test",
	})
	require.NoError(t, err)

	conflictBody := replaceCCSwitchBootstrapBodyField(secondBody, "other-device-fingerprint", "stable-device-fingerprint")
	_, err = HandleCCSwitchBootstrap(context.Background(), CCSwitchBootstrapRequestContext{
		Method:    http.MethodPost,
		Path:      CCSwitchBootstrapPath,
		Body:      []byte(conflictBody),
		Headers:   signedCCSwitchBootstrapHeaders(t, conflictBody, "nonce-conflict-3"),
		ClientIP:  "203.0.113.12",
		UserAgent: "cc-switch-test",
	})
	requireBootstrapStatus(t, err, http.StatusConflict)
}

func TestCCSwitchBootstrapRejectsBadSignatureAndNonceReplay(t *testing.T) {
	setupCCSwitchBootstrapServiceTest(t)

	headers := signedCCSwitchBootstrapHeaders(t, ccSwitchBootstrapTestBody, "nonce-replay")
	headers.Signature = strings.Repeat("0", 64)
	_, err := HandleCCSwitchBootstrap(context.Background(), CCSwitchBootstrapRequestContext{
		Method:    http.MethodPost,
		Path:      CCSwitchBootstrapPath,
		Body:      []byte(ccSwitchBootstrapTestBody),
		Headers:   headers,
		ClientIP:  "203.0.113.10",
		UserAgent: "cc-switch-test",
	})
	requireBootstrapStatus(t, err, http.StatusUnauthorized)

	headers = signedCCSwitchBootstrapHeaders(t, ccSwitchBootstrapTestBody, "nonce-replay")
	_, err = HandleCCSwitchBootstrap(context.Background(), CCSwitchBootstrapRequestContext{
		Method:    http.MethodPost,
		Path:      CCSwitchBootstrapPath,
		Body:      []byte(ccSwitchBootstrapTestBody),
		Headers:   headers,
		ClientIP:  "203.0.113.10",
		UserAgent: "cc-switch-test",
	})
	require.NoError(t, err)

	_, err = HandleCCSwitchBootstrap(context.Background(), CCSwitchBootstrapRequestContext{
		Method:    http.MethodPost,
		Path:      CCSwitchBootstrapPath,
		Body:      []byte(ccSwitchBootstrapTestBody),
		Headers:   headers,
		ClientIP:  "203.0.113.10",
		UserAgent: "cc-switch-test",
	})
	requireBootstrapStatus(t, err, http.StatusUnauthorized)
}

func TestCCSwitchBootstrapDisabledIgnoresMalformedClientsConfig(t *testing.T) {
	setupCCSwitchBootstrapServiceTest(t)
	setCCSwitchBootstrapTestEnv(t, map[string]string{
		"CC_SWITCH_BOOTSTRAP_ENABLED": "false",
		"CC_SWITCH_BOOTSTRAP_CLIENTS": "{not-json",
	})

	_, err := HandleCCSwitchBootstrap(context.Background(), CCSwitchBootstrapRequestContext{
		Method:    http.MethodPost,
		Path:      CCSwitchBootstrapPath,
		Body:      []byte(ccSwitchBootstrapTestBody),
		Headers:   signedCCSwitchBootstrapHeaders(t, ccSwitchBootstrapTestBody, "nonce-disabled-malformed"),
		ClientIP:  "203.0.113.10",
		UserAgent: "cc-switch-test",
	})
	requireBootstrapStatus(t, err, http.StatusForbidden)
	require.NotContains(t, err.Error(), "invalid bootstrap clients")
}

func TestCCSwitchBootstrapDatabaseErrorsAreSanitized(t *testing.T) {
	err := errDatabase(errors.New("SQL duplicate key contains test-secret and bootstrap_devices.install_id_hash"))

	requireBootstrapStatus(t, err, http.StatusInternalServerError)
	require.NotContains(t, err.Error(), "test-secret")
	require.NotContains(t, err.Error(), "bootstrap_devices")
	require.NotContains(t, err.Error(), "install_id_hash")
}

func TestCCSwitchBootstrapConcurrentSameDeviceIsIdempotent(t *testing.T) {
	db := setupCCSwitchBootstrapServiceTest(t)

	const requestCount = 20
	var wg sync.WaitGroup
	results := make(chan *CCSwitchBootstrapResult, requestCount)
	errs := make(chan error, requestCount)

	for i := 0; i < requestCount; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			result, err := HandleCCSwitchBootstrap(context.Background(), CCSwitchBootstrapRequestContext{
				Method:    http.MethodPost,
				Path:      CCSwitchBootstrapPath,
				Body:      []byte(ccSwitchBootstrapTestBody),
				Headers:   signedCCSwitchBootstrapHeaders(t, ccSwitchBootstrapTestBody, fmt.Sprintf("nonce-concurrent-%d", index)),
				ClientIP:  "203.0.113.10",
				UserAgent: "cc-switch-test",
			})
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)

	var firstKey string
	successCount := 0
	for result := range results {
		successCount++
		require.NotEmpty(t, result.Provider.APIKey)
		if firstKey == "" {
			firstKey = result.Provider.APIKey
		}
		require.Equal(t, firstKey, result.Provider.APIKey)
	}
	require.Greater(t, successCount, 0)
	for err := range errs {
		requireBootstrapStatus(t, err, http.StatusTooManyRequests)
	}

	var userCount int64
	var deviceCount int64
	var tokenCount int64
	require.NoError(t, db.Model(&model.User{}).Count(&userCount).Error)
	require.NoError(t, db.Model(&model.BootstrapDevice{}).Count(&deviceCount).Error)
	require.NoError(t, db.Model(&model.Token{}).Count(&tokenCount).Error)
	require.EqualValues(t, 1, userCount)
	require.EqualValues(t, 1, deviceCount)
	require.EqualValues(t, 1, tokenCount)
}

func requireBootstrapStatus(t *testing.T, err error, status int) {
	t.Helper()
	require.Error(t, err)
	var bootstrapErr *CCSwitchBootstrapError
	require.ErrorAs(t, err, &bootstrapErr)
	require.Equal(t, status, bootstrapErr.StatusCode)
}
