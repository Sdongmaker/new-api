package controller

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type ccSwitchBootstrapAPIResponse struct {
	Success bool                            `json:"success"`
	Message string                          `json:"message"`
	Data    service.CCSwitchBootstrapResult `json:"data"`
}

type ccSwitchBootstrapClaimLinkAPIResponse struct {
	Success bool                                     `json:"success"`
	Message string                                   `json:"message"`
	Data    service.CCSwitchBootstrapClaimLinkResult `json:"data"`
}

type ccSwitchBootstrapClaimAPIResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    struct {
		AccessToken     string `json:"access_token"`
		TokenType       string `json:"token_type"`
		AccessExpiresAt int64  `json:"access_expires_at"`
		Session         struct {
			SID string `json:"sid"`
		} `json:"session"`
		User struct {
			Id       int    `json:"id"`
			Username string `json:"username"`
			Role     int    `json:"role"`
			Status   int    `json:"status"`
			Group    string `json:"group"`
		} `json:"user"`
		RedirectPath      string `json:"redirect_path"`
		NeedsProfileSetup bool   `json:"needs_profile_setup"`
		ProfileSetupToken string `json:"profile_setup_token"`
	} `json:"data"`
}

const ccSwitchBootstrapControllerTestBody = `{"install_id":"8e8b6a40-4214-44cb-b82e-4eecf09f42e8","device_fingerprint":"v1:macos:stable-device-fingerprint","client_version":"3.14.1-proprietary.1","platform":"macos","arch":"aarch64","build_channel":"proprietary"}`

func setupCCSwitchBootstrapControllerTest(t *testing.T) *gorm.DB {
	t.Helper()
	gin.SetMode(gin.TestMode)
	originalDB := model.DB
	originalLOGDB := model.LOG_DB
	originalMainDatabaseType := common.MainDatabaseType()
	originalLogDatabaseType := common.LogDatabaseType()
	originalRedisEnabled := common.RedisEnabled
	originalRegisterEnabled := common.RegisterEnabled
	originalQuotaForNewUser := common.QuotaForNewUser
	originalSessionSecret := common.SessionSecret

	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	common.RegisterEnabled = false
	common.QuotaForNewUser = 12345
	common.SessionSecret = "cc-switch-bootstrap-test-session-secret"

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Log{}, &model.BootstrapDevice{}, &model.BootstrapClaimTicket{}, &model.UserSession{}, &model.AuthFlow{}))

	setCCSwitchBootstrapControllerTestEnv(t, map[string]string{
		"CC_SWITCH_BOOTSTRAP_ENABLED":           "true",
		"CC_SWITCH_BOOTSTRAP_CLIENTS":           `{"cc-switch-proprietary":"test-secret"}`,
		"CC_SWITCH_BOOTSTRAP_PROVIDER_BASE_URL": "https://api.example.com",
		"CC_SWITCH_BOOTSTRAP_SERVER_SALT":       "test-bootstrap-salt",
	})

	t.Cleanup(func() {
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
		common.SessionSecret = originalSessionSecret
	})

	return db
}

func setCCSwitchBootstrapControllerTestEnv(t *testing.T, values map[string]string) {
	t.Helper()
	for key, value := range values {
		t.Setenv(key, value)
	}
}

func signCCSwitchControllerBody(t *testing.T, body string, nonce string) map[string]string {
	t.Helper()
	return signCCSwitchControllerBodyForPath(t, service.CCSwitchBootstrapPath, body, nonce)
}

func signCCSwitchControllerBodyForPath(t *testing.T, path string, body string, nonce string) map[string]string {
	t.Helper()
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	bodyHash := sha256.Sum256([]byte(body))
	signingString := fmt.Sprintf("%s\n%s\n%s\n%s\n%s", http.MethodPost, path, timestamp, nonce, hex.EncodeToString(bodyHash[:]))
	mac := hmac.New(sha256.New, []byte("test-secret"))
	_, err := mac.Write([]byte(signingString))
	require.NoError(t, err)
	return map[string]string{
		"X-CCS-Client-Id": "cc-switch-proprietary",
		"X-CCS-Timestamp": timestamp,
		"X-CCS-Nonce":     nonce,
		"X-CCS-Signature": hex.EncodeToString(mac.Sum(nil)),
	}
}

func performCCSwitchBootstrapRequest(t *testing.T, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, service.CCSwitchBootstrapPath, strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		ctx.Request.Header.Set(key, value)
	}
	CCSwitchBootstrap(ctx)
	return recorder
}

func performCCSwitchClaimLinkRequest(t *testing.T, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, service.CCSwitchBootstrapClaimLinkPath, strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		ctx.Request.Header.Set(key, value)
	}
	CCSwitchBootstrapClaimLink(ctx)
	return recorder
}

func performCCSwitchClaimRequest(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	return performCCSwitchClaimRequestWithEngine(t, newCCSwitchClaimTestEngine(), body)
}

func newCCSwitchClaimTestEngine() *gin.Engine {
	engine := gin.New()
	engine.POST("/api/bootstrap/cc-switch/claim", CCSwitchBootstrapClaim)
	selfRoute := engine.Group("/api/user")
	selfRoute.Use(middleware.UserAuth())
	selfRoute.PUT("/self", UpdateSelf)
	return engine
}

func performCCSwitchClaimRequestWithEngine(t *testing.T, engine *gin.Engine, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/bootstrap/cc-switch/claim", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(recorder, req)
	return recorder
}

func TestCCSwitchBootstrapDisabledReturnsForbidden(t *testing.T) {
	setupCCSwitchBootstrapControllerTest(t)
	setCCSwitchBootstrapControllerTestEnv(t, map[string]string{
		"CC_SWITCH_BOOTSTRAP_ENABLED": "false",
	})

	recorder := performCCSwitchBootstrapRequest(t, ccSwitchBootstrapControllerTestBody, nil)
	require.Equal(t, http.StatusForbidden, recorder.Code)

	var response ccSwitchBootstrapAPIResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.False(t, response.Success)
	require.Contains(t, response.Message, "bootstrap disabled")
}

func TestCCSwitchBootstrapSuccessResponseDoesNotLeakLoginFields(t *testing.T) {
	setupCCSwitchBootstrapControllerTest(t)
	headers := signCCSwitchControllerBody(t, ccSwitchBootstrapControllerTestBody, "controller-success")

	recorder := performCCSwitchBootstrapRequest(t, ccSwitchBootstrapControllerTestBody, headers)
	require.Equal(t, http.StatusOK, recorder.Code)

	var response ccSwitchBootstrapAPIResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	require.Equal(t, service.CCSwitchBootstrapActionCreated, response.Data.Action)
	require.True(t, strings.HasPrefix(response.Data.Provider.APIKey, "sk-"))
	require.NotContains(t, recorder.Body.String(), "password")
	require.NotContains(t, recorder.Body.String(), "access_token")
	require.NotContains(t, recorder.Body.String(), "username")
	require.Empty(t, recorder.Result().Cookies())
}

func TestCCSwitchBootstrapClaimLinkAndClaimLogin(t *testing.T) {
	db := setupCCSwitchBootstrapControllerTest(t)
	bootstrapHeaders := signCCSwitchControllerBody(t, ccSwitchBootstrapControllerTestBody, "controller-claim-bootstrap")
	bootstrapRecorder := performCCSwitchBootstrapRequest(t, ccSwitchBootstrapControllerTestBody, bootstrapHeaders)
	require.Equal(t, http.StatusOK, bootstrapRecorder.Code)

	claimLinkBody := strings.TrimSuffix(ccSwitchBootstrapControllerTestBody, "}") + `,"redirect_path":"/console/topup"}`
	claimLinkHeaders := signCCSwitchControllerBodyForPath(t, service.CCSwitchBootstrapClaimLinkPath, claimLinkBody, "controller-claim-link")
	claimLinkRecorder := performCCSwitchClaimLinkRequest(t, claimLinkBody, claimLinkHeaders)
	require.Equal(t, http.StatusOK, claimLinkRecorder.Code)

	var claimLinkResponse ccSwitchBootstrapClaimLinkAPIResponse
	require.NoError(t, common.Unmarshal(claimLinkRecorder.Body.Bytes(), &claimLinkResponse))
	require.True(t, claimLinkResponse.Success)
	require.NotContains(t, claimLinkRecorder.Body.String(), "sk-")

	parsed, err := url.Parse(claimLinkResponse.Data.ClaimURL)
	require.NoError(t, err)
	require.Empty(t, parsed.RawQuery)
	fragment, err := url.ParseQuery(parsed.Fragment)
	require.NoError(t, err)
	ticket := fragment.Get("ticket")
	require.NotEmpty(t, ticket)

	claimEngine := newCCSwitchClaimTestEngine()
	claimRecorder := performCCSwitchClaimRequestWithEngine(t, claimEngine, fmt.Sprintf(`{"ticket":%q}`, ticket))
	require.Equal(t, http.StatusOK, claimRecorder.Code)

	var claimResponse ccSwitchBootstrapClaimAPIResponse
	require.NoError(t, common.Unmarshal(claimRecorder.Body.Bytes(), &claimResponse))
	require.True(t, claimResponse.Success)
	require.True(t, claimResponse.Data.NeedsProfileSetup)
	require.Equal(t, "/console/topup", claimResponse.Data.RedirectPath)
	require.True(t, strings.HasPrefix(claimResponse.Data.User.Username, "ccs_"))
	require.NotContains(t, claimRecorder.Body.String(), "password")
	assert.NotEmpty(t, claimResponse.Data.AccessToken)
	assert.NotEmpty(t, claimResponse.Data.Session.SID)
	assert.NotEmpty(t, claimResponse.Data.ProfileSetupToken)
	assert.NotEmpty(t, claimRecorder.Result().Cookies())

	profileBody := fmt.Sprintf(`{"username":"claimed-user","display_name":"claimed-user","password":"claimed-pass","profile_setup_token":%q}`, claimResponse.Data.ProfileSetupToken)
	profileRecorder := httptest.NewRecorder()
	profileRequest := httptest.NewRequest(http.MethodPut, "/api/user/self", strings.NewReader(profileBody))
	profileRequest.Header.Set("Content-Type", "application/json")
	profileRequest.Header.Set("Authorization", "Bearer "+claimResponse.Data.AccessToken)
	claimEngine.ServeHTTP(profileRecorder, profileRequest)
	require.Equal(t, http.StatusOK, profileRecorder.Code)

	var profileResponse struct {
		Success bool `json:"success"`
		Data    struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(profileRecorder.Body.Bytes(), &profileResponse))
	require.True(t, profileResponse.Success)
	assert.NotEmpty(t, profileResponse.Data.AccessToken)

	var updatedUser model.User
	require.NoError(t, db.First(&updatedUser, claimResponse.Data.User.Id).Error)
	assert.Equal(t, "claimed-user", updatedUser.Username)
	require.True(t, common.ValidatePasswordAndHash("claimed-pass", updatedUser.Password))

	// 设置过初始密码后，即使再签发一个全新的 profile_setup_token 也不能绕过
	// 原密码校验：绕过只对“尚未设置密码”的匿名用户生效。
	freshSetupToken, _, err := model.CreateAuthFlow(model.AuthFlowCreate{
		Purpose:   model.AuthFlowPurposeCCSwitchProfileSetup,
		UserId:    claimResponse.Data.User.Id,
		ExpiresAt: time.Now().Add(10 * time.Minute),
	})
	require.NoError(t, err)
	secondPasswordRecorder := httptest.NewRecorder()
	secondPasswordBody := fmt.Sprintf(`{"username":"claimed-user","display_name":"claimed-user","password":"second-pass","profile_setup_token":%q}`, freshSetupToken)
	secondPasswordRequest := httptest.NewRequest(http.MethodPut, "/api/user/self", strings.NewReader(secondPasswordBody))
	secondPasswordRequest.Header.Set("Content-Type", "application/json")
	secondPasswordRequest.Header.Set("Authorization", "Bearer "+profileResponse.Data.AccessToken)
	claimEngine.ServeHTTP(secondPasswordRecorder, secondPasswordRequest)
	require.Equal(t, http.StatusOK, secondPasswordRecorder.Code)

	var secondPasswordResponse struct {
		Success bool `json:"success"`
	}
	require.NoError(t, common.Unmarshal(secondPasswordRecorder.Body.Bytes(), &secondPasswordResponse))
	require.False(t, secondPasswordResponse.Success)
	require.NoError(t, db.First(&updatedUser, claimResponse.Data.User.Id).Error)
	assert.True(t, common.ValidatePasswordAndHash("claimed-pass", updatedUser.Password))

	replayRecorder := performCCSwitchClaimRequest(t, fmt.Sprintf(`{"ticket":%q}`, ticket))
	require.Equal(t, http.StatusUnauthorized, replayRecorder.Code)
}

func TestCCSwitchBootstrapRejectsOversizedBodies(t *testing.T) {
	setupCCSwitchBootstrapControllerTest(t)
	oversized := strings.Repeat("a", 20*1024)

	bootstrapRecorder := performCCSwitchBootstrapRequest(t, oversized, map[string]string{})
	require.Equal(t, http.StatusBadRequest, bootstrapRecorder.Code)

	claimLinkRecorder := performCCSwitchClaimLinkRequest(t, oversized, map[string]string{})
	require.Equal(t, http.StatusBadRequest, claimLinkRecorder.Code)

	claimRecorder := performCCSwitchClaimRequest(t, oversized)
	require.Equal(t, http.StatusBadRequest, claimRecorder.Code)
}
