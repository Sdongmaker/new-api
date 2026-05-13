package controller

import (
	"errors"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func CCSwitchBootstrap(c *gin.Context) {
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
