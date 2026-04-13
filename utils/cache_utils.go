package utils

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"mindex-backend/config"
	"strings"
	"time"
)

// GenerateCacheKey tạo key redis chuẩn hóa cho query
func GenerateCacheKey(prefix string, query string, subject string) string {
	// Chuẩn hóa query
	q := strings.ToLower(strings.TrimSpace(query))
	
	// Tạo hash md5 của query để key không quá dài
	hash := md5.Sum([]byte(q))
	queryHash := hex.EncodeToString(hash[:])
	
	if subject != "" {
		return fmt.Sprintf("cache:community:%s:%s:%s", prefix, queryHash, subject)
	}
	return fmt.Sprintf("cache:community:%s:%s", prefix, queryHash)
}

// GetCache lấy dữ liệu từ Redis (trả về chuỗi rỗng nếu lỗi hoặc không có)
func GetCache(key string) string {
	if config.RedisClient == nil {
		return ""
	}
	val, err := config.RedisClient.Get(config.Ctx, key).Result()
	if err != nil {
		return ""
	}
	return val
}

// SetCache lưu dữ liệu vào Redis
func SetCache(key string, value string, expiration time.Duration) {
	if config.RedisClient == nil {
		return
	}
	config.RedisClient.Set(config.Ctx, key, value, expiration)
}
