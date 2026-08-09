package controller

import (
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

const (
	ccSwitchBootstrapMaxBodyBytes = 16 * 1024
)

func CCSwitchBootstrap(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, ccSwitchBootstrapMaxBodyBytes)
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "invalid request body",
		})
		return
	}
	result, err := service.HandleCCSwitchBootstrap(c.Request.Context(), service.CCSwitchBootstrapRequestContext{
		Method: c.Request.Method,
		Path:   c.Request.URL.Path,
		Body:   body,
		Headers: service.CCSwitchBootstrapHeaders{
			ClientID:  c.GetHeader("X-CCS-Client-Id"),
			Timestamp: c.GetHeader("X-CCS-Timestamp"),
			Nonce:     c.GetHeader("X-CCS-Nonce"),
			Signature: c.GetHeader("X-CCS-Signature"),
		},
		ClientIP:  c.ClientIP(),
		UserAgent: c.Request.UserAgent(),
	})
	if err != nil {
		statusCode := http.StatusInternalServerError
		var bootstrapErr *service.CCSwitchBootstrapError
		if errors.As(err, &bootstrapErr) {
			statusCode = bootstrapErr.StatusCode
		}
		c.JSON(statusCode, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    result,
	})
}

func CCSwitchBootstrapClaimLink(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, ccSwitchBootstrapMaxBodyBytes)
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "invalid request body",
		})
		return
	}
	result, err := service.HandleCCSwitchBootstrapClaimLink(c.Request.Context(), service.CCSwitchBootstrapRequestContext{
		Method: c.Request.Method,
		Path:   c.Request.URL.Path,
		Body:   body,
		Headers: service.CCSwitchBootstrapHeaders{
			ClientID:  c.GetHeader("X-CCS-Client-Id"),
			Timestamp: c.GetHeader("X-CCS-Timestamp"),
			Nonce:     c.GetHeader("X-CCS-Nonce"),
			Signature: c.GetHeader("X-CCS-Signature"),
		},
		ClientIP:  c.ClientIP(),
		UserAgent: c.Request.UserAgent(),
	})
	if err != nil {
		writeCCSwitchBootstrapError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    result,
	})
}

type ccSwitchBootstrapClaimRequest struct {
	Ticket string `json:"ticket"`
}

func CCSwitchBootstrapClaim(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, ccSwitchBootstrapMaxBodyBytes)
	var req ccSwitchBootstrapClaimRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "invalid request body",
		})
		return
	}
	result, err := service.ConsumeCCSwitchBootstrapClaimTicket(req.Ticket)
	if err != nil {
		writeCCSwitchBootstrapError(c, err)
		return
	}
	// 匿名用户认领后需要设置用户名/密码。签发一次性 profile_setup_token，
	// 允许 UpdateSelf 在无原始密码的情况下设置初始密码（用完即焚）。
	profileSetupToken := ""
	if result.NeedsProfileSetup {
		expiresAt := time.Now().Add(10 * time.Minute)
		flowToken, _, flowErr := model.CreateAuthFlow(model.AuthFlowCreate{
			Purpose:   model.AuthFlowPurposeCCSwitchProfileSetup,
			UserId:    result.User.Id,
			ExpiresAt: expiresAt,
		})
		if flowErr != nil {
			common.ApiError(c, flowErr)
			return
		}
		profileSetupToken = flowToken
	}
	bundle, err := service.CreateLoginSession(
		result.User.Id,
		loginMethodFromContext(c),
		c.ClientIP(),
		c.Request.UserAgent(),
	)
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	model.UpdateUserLastLoginAt(result.User.Id)
	service.WriteRefreshCookie(c, bundle.RefreshToken)
	setAuthNoStore(c)
	recordLoginAudit(&result.User, c)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"access_token":      bundle.AccessToken,
			"token_type":        bundle.TokenType,
			"access_expires_at": bundle.AccessExpiresAt,
			"session":           bundle.Session,
			"user": gin.H{
				"id":           result.User.Id,
				"username":     result.User.Username,
				"display_name": result.User.DisplayName,
				"role":         result.User.Role,
				"status":       result.User.Status,
				"group":        result.User.Group,
			},
			"redirect_path":       result.RedirectPath,
			"needs_profile_setup": result.NeedsProfileSetup,
			"profile_setup_token": profileSetupToken,
		},
	})
}

func writeCCSwitchBootstrapError(c *gin.Context, err error) {
	statusCode := http.StatusInternalServerError
	var bootstrapErr *service.CCSwitchBootstrapError
	if errors.As(err, &bootstrapErr) {
		statusCode = bootstrapErr.StatusCode
	}
	c.JSON(statusCode, gin.H{
		"success": false,
		"message": err.Error(),
	})
}
