package middleware

import (
	"mindex-backend/config"
	"net/http"

	"github.com/gin-gonic/gin"
)

func RequireTrustedOrigin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodOptions || !isUnsafeMethod(c.Request.Method) || isCSRFExemptPath(c.Request.URL.Path) {
			c.Next()
			return
		}

		if !config.IsAllowedOrigin(c.GetHeader("Origin")) {
			c.JSON(http.StatusForbidden, gin.H{
				"success": false,
				"error":   "CSRF_ORIGIN_DENIED",
				"message": "Request origin is not trusted.",
			})
			c.Abort()
			return
		}

		c.Next()
	}
}

func isUnsafeMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func isCSRFExemptPath(path string) bool {
	return path == "/api/v1/billings/webhook"
}
