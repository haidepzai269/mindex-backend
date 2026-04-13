package middleware

import (
	"mindex-backend/config"
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
		} else if qToken := c.Query("token"); qToken != "" {
			// Đặc biệt cho EventSource/SSE không gửi được header
			tokenString = qToken
		} else {
			// Fallback: Thử lấy từ cookie
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

		// KIỂM TRA BLACKLIST: Nếu JTI của token nằm trong Redis thì coi như token đã bị vô hiệu hóa
		if config.RedisClient != nil {
			isBlacklisted, _ := config.RedisClient.Exists(config.Ctx, "blacklist:"+claims.ID).Result()
			if isBlacklisted > 0 {
				c.JSON(401, gin.H{"success": false, "error": "UNAUTHORIZED", "message": "Phiên đăng nhập đã kết thúc, vui lòng đăng nhập lại"})
				c.Abort()
				return
			}
		}

		c.Set("user_id", claims.UserID)
		c.Set("token_id", claims.ID) // Lưu lại để dùng khi logout
		c.Set("token_exp", claims.ExpiresAt.Unix())
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
