package middleware

import (
	"mindex-backend/config"
	"mindex-backend/utils"
	"strings"

	"github.com/gin-gonic/gin"
)

func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == "OPTIONS" {
			c.Next()
			return
		}

		authHeader := c.GetHeader("Authorization")
		tokenString := ""

		if authHeader != "" && strings.HasPrefix(authHeader, "Bearer ") {
			tokenString = strings.TrimPrefix(authHeader, "Bearer ")
		} else {
			cookieToken, err := c.Cookie("access_token")
			if err == nil {
				tokenString = cookieToken
			}
		}

		if tokenString == "" {
			c.JSON(401, gin.H{"success": false, "error": "UNAUTHORIZED", "message": "Chua dang nhap"})
			c.Abort()
			return
		}

		claims, err := utils.VerifyToken(tokenString, false)
		if err != nil {
			c.JSON(401, gin.H{"success": false, "error": "UNAUTHORIZED", "message": "Token het han hoac khong hop le"})
			c.Abort()
			return
		}

		if config.RedisClient != nil {
			isBlacklisted, err := config.RedisClient.Exists(config.Ctx, "blacklist:"+claims.ID).Result()
			if err != nil {
				c.JSON(503, gin.H{"success": false, "error": "AUTH_STATE_UNAVAILABLE", "message": "Authentication service unavailable"})
				c.Abort()
				return
			}
			if isBlacklisted > 0 {
				c.JSON(401, gin.H{"success": false, "error": "UNAUTHORIZED", "message": "Phien dang nhap da ket thuc, vui long dang nhap lai"})
				c.Abort()
				return
			}
		}

		c.Set("user_id", claims.UserID)
		c.Set("token_id", claims.ID)
		c.Set("token_exp", claims.ExpiresAt.Unix())
		c.Set("role", claims.Role)
		c.Set("persona", claims.Persona)
		c.Next()
	}
}

func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetString("user_id")
		if userID == "" {
			c.JSON(401, gin.H{"success": false, "error": "UNAUTHORIZED", "message": "Chua dang nhap"})
			c.Abort()
			return
		}

		var dbRole string
		err := config.DB.QueryRow(config.Ctx, "SELECT COALESCE(role, 'user') FROM users WHERE id = $1", userID).Scan(&dbRole)
		if err != nil || dbRole != "admin" {
			c.JSON(403, gin.H{"success": false, "error": "FORBIDDEN", "message": "Yeu cau quyen admin"})
			c.Abort()
			return
		}
		c.Next()
	}
}
