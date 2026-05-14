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
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
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
		User struct {
			Id       int    `json:"id"`
			Username string `json:"username"`
			Role     int    `json:"role"`
			Status   int    `json:"status"`
			Group    string `json:"group"`
		} `json:"user"`
		RedirectPath      string `json:"redirect_path"`
		NeedsProfileSetup bool   `json:"needs_profile_setup"`
	} `json:"data"`
}

const ccSwitchBootstrapControllerTestBody = `{"install_id":"8e8b6a40-4214-44cb-b82e-4eecf09f42e8","device_fingerprint":"v1:macos:stable-device-fingerprint","client_version":"3.14.1-proprietary.1","platform":"macos","arch":"aarch64","build_channel":"proprietary"}`

func setupCCSwitchBootstrapControllerTest(t *testing.T) *gorm.DB {
	t.Helper()
	gin.SetMode(gin.TestMode)
	originalDB := model.DB
	originalLOGDB := model.LOG_DB
	originalUsingSQLite := common.UsingSQLite
	originalUsingMySQL := common.UsingMySQL
	originalUsingPostgreSQL := common.UsingPostgreSQL
	originalRedisEnabled := common.RedisEnabled
	originalRegisterEnabled := common.RegisterEnabled
	originalQuotaForNewUser := common.QuotaForNewUser

	common.UsingSQLite = true
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	common.RedisEnabled = false
	common.RegisterEnabled = false
	common.QuotaForNewUser = 12345

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Log{}, &model.BootstrapDevice{}, &model.BootstrapClaimTicket{}))

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
		common.UsingSQLite = originalUsingSQLite
		common.UsingMySQL = originalUsingMySQL
		common.UsingPostgreSQL = originalUsingPostgreSQL
		common.RedisEnabled = originalRedisEnabled
		common.RegisterEnabled = originalRegisterEnabled
		common.QuotaForNewUser = originalQuotaForNewUser
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
	store := cookie.NewStore([]byte("test-session-secret"))
	engine.Use(sessions.Sessions("session", store))
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
	ticket := parsed.Query().Get("ticket")
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
	require.NotEmpty(t, claimRecorder.Result().Cookies())

	profileRecorder := httptest.NewRecorder()
	profileRequest := httptest.NewRequest(http.MethodPut, "/api/user/self", strings.NewReader(`{"username":"claimed-user","display_name":"claimed-user","password":"claimed-pass"}`))
	profileRequest.Header.Set("Content-Type", "application/json")
	profileRequest.Header.Set("New-Api-User", fmt.Sprintf("%d", claimResponse.Data.User.Id))
	for _, responseCookie := range claimRecorder.Result().Cookies() {
		profileRequest.AddCookie(responseCookie)
	}
	claimEngine.ServeHTTP(profileRecorder, profileRequest)
	require.Equal(t, http.StatusOK, profileRecorder.Code)

	var profileResponse struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	require.NoError(t, common.Unmarshal(profileRecorder.Body.Bytes(), &profileResponse))
	require.True(t, profileResponse.Success)

	var updatedUser model.User
	require.NoError(t, db.First(&updatedUser, claimResponse.Data.User.Id).Error)
	require.Equal(t, "claimed-user", updatedUser.Username)
	require.True(t, common.ValidatePasswordAndHash("claimed-pass", updatedUser.Password))

	replayRecorder := performCCSwitchClaimRequest(t, fmt.Sprintf(`{"ticket":%q}`, ticket))
	require.Equal(t, http.StatusUnauthorized, replayRecorder.Code)
}
