package controller

import (
	"errors"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

const ccSwitchBootstrapMaxBodyBytes = 16 * 1024

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
	if err := setupLoginSession(&result.User, c); err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
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
