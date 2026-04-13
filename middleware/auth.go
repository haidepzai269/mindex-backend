package middleware

import (
	"mindex-backend/utils"
	"strings"

	"github.com/gin-gonic/gin"
)

func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Bỏ qua xác thực cho phương thức OPTIONS (CORS preflight)
		if c.Request.Method == "OPTIONS" {
			c.Next()
			return
		}

		authHeader := c.GetHeader("Authorization")
		tokenString := ""

		if authHeader != "" && strings.HasPrefix(authHeader, "Bearer ") {
			tokenString = strings.TrimPrefix(authHeader, "Bearer ")
		} else {
			// Fallback: Thử lấy từ cookie (đặc biệt cho EventSource/SSE)
			cookieToken, err := c.Cookie("access_token")
			if err == nil {
				tokenString = cookieToken
			}
		}

		if tokenString == "" {
			c.JSON(401, gin.H{"success": false, "error": "UNAUTHORIZED", "message": "Chưa đăng nhập"})
			c.Abort()
			return
		}

		claims, err := utils.VerifyToken(tokenString, false)
		if err != nil {
			c.JSON(401, gin.H{"success": false, "error": "UNAUTHORIZED", "message": "Token hết hạn hoặc không hợp lệ"})
			c.Abort()
			return
		}

		c.Set("user_id", claims.UserID)
		c.Set("role", claims.Role)
		c.Set("persona", claims.Persona)
		c.Next()
	}
}

func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		role := c.GetString("role")
		if role != "admin" {
			c.JSON(403, gin.H{"success": false, "error": "FORBIDDEN", "message": "Yêu cầu quyền admin"})
			c.Abort()
			return
		}
		c.Next()
	}
}
