package config

import (
	"context"
	"log"

	"github.com/redis/go-redis/v9"
)

var RedisClient *redis.Client
var Ctx = context.Background()

func ConnectRedis() {
	opt, err := redis.ParseURL(Env.RedisURL)
	if err != nil {
		log.Fatalf("Không thể parse Redis URL: %v", err)
	}

	RedisClient = redis.NewClient(opt)

	err = RedisClient.Ping(Ctx).Err()
	if err != nil {
		log.Printf("⚠️ Lỗi kết nối Redis: %v. Hệ thống vẫn tiếp tục chạy với DB.", err)
		RedisClient = nil
	} else {
		log.Println("✅ Đã kết nối thành công tới Redis")
	}
}

func CloseRedis() {
	if RedisClient != nil {
		RedisClient.Close()
		log.Println("✅ Đã ngắt kết nối Redis an toàn")
	}
}
