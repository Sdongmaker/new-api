package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCCSwitchBootstrapRouteIsPublic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalGlobalRateLimit := common.GlobalApiRateLimitEnable
	originalRedisEnabled := common.RedisEnabled
	common.GlobalApiRateLimitEnable = false
	common.RedisEnabled = false
	t.Setenv("CC_SWITCH_BOOTSTRAP_ENABLED", "false")
	t.Cleanup(func() {
		common.GlobalApiRateLimitEnable = originalGlobalRateLimit
		common.RedisEnabled = originalRedisEnabled
	})

	engine := gin.New()
	SetApiRouter(engine)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, service.CCSwitchBootstrapPath, strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), "bootstrap disabled")
	require.NotContains(t, recorder.Body.String(), "未登录")
	require.NotContains(t, recorder.Body.String(), "unauthorized")
}
