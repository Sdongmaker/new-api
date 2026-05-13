package controller

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
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
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Log{}, &model.BootstrapDevice{}))

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
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	bodyHash := sha256.Sum256([]byte(body))
	signingString := fmt.Sprintf("%s\n%s\n%s\n%s\n%s", http.MethodPost, service.CCSwitchBootstrapPath, timestamp, nonce, hex.EncodeToString(bodyHash[:]))
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
