package utils

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"log"
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

// ClearCommunityCache xóa toàn bộ cache kết quả tìm kiếm/duyệt của Community
func ClearCommunityCache() {
	if config.RedisClient == nil {
		return
	}
	
	// Sử dụng SCAN để tìm các key có prefix cụ thể và xóa chúng
	// Key format: cache:community:results:*
	ctx := config.Ctx
	var cursor uint64
	var keys []string
	var err error

	for {
		keys, cursor, err = config.RedisClient.Scan(ctx, cursor, "cache:community:*", 100).Result()
		if err != nil {
			log.Printf("⚠️ [Cache] Lỗi SCAN community cache: %v", err)
			break
		}

		if len(keys) > 0 {
			log.Printf("🧹 [Cache] Đang xóa %d keys community: %v", len(keys), keys)
			config.RedisClient.Del(ctx, keys...)
		}

		if cursor == 0 {
			break
		}
	}
	
	log.Printf("🧹 [Cache] Đã hoàn tất làm mới Community Library cache")
}
