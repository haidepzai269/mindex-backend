package middleware

import (
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"mindex-backend/config"
)

// RateLimit giới hạn số request trong một khoảng thời gian, dùng Redis sliding window
// key: tiền tố phân biệt (vd: "login", "ai")
// limit: số request tối đa
// window: khoảng thời gian
func RateLimit(key string, limit int, window time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		if config.RedisClient == nil {
			c.Next()
			return
		}

		// Ưu tiên theo userID (đã auth), fallback về IP
		identifier := c.GetString("user_id")
		if identifier == "" {
			identifier = c.ClientIP()
		}

		redisKey := fmt.Sprintf("ratelimit:%s:%s", key, identifier)
		now := time.Now()

		pipe := config.RedisClient.Pipeline()
		// Xóa các entry cũ ngoài cửa sổ
		pipe.ZRemRangeByScore(config.Ctx, redisKey, "0",
			fmt.Sprintf("%d", now.Add(-window).UnixMilli()))
		// Đếm số request hiện tại
		countCmd := pipe.ZCard(config.Ctx, redisKey)
		// Thêm request hiện tại
		pipe.ZAdd(config.Ctx, redisKey, redis.Z{Score: float64(now.UnixMilli()), Member: now.UnixNano()})
		// Đặt TTL bằng khoảng window
		pipe.Expire(config.Ctx, redisKey, window)
		pipe.Exec(config.Ctx)

		count := countCmd.Val()
		if count >= int64(limit) {
			c.JSON(429, gin.H{
				"success": false,
				"error":   "RATE_LIMIT_EXCEEDED",
				"message": fmt.Sprintf("Quá nhiều yêu cầu. Vui lòng chờ %d giây.", int(window.Seconds())),
			})
			c.Abort()
			return
		}

		c.Next()
	}
}
