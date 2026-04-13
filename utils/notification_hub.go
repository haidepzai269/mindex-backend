package utils

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"mindex-backend/config"
	"mindex-backend/models"
)

// PublishNotification thực hiện lưu thông báo vào DB và đẩy qua Redis PubSub
func PublishNotification(userID string, nType string, title string, message string, data interface{}) error {
	dataBytes, _ := json.Marshal(data)
	
	// 1. Lưu vào Database (Persistence)
	var n models.Notification
	err := config.DB.QueryRow(context.Background(), `
		INSERT INTO notifications (user_id, type, title, message, data)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at`,
		userID, nType, title, message, dataBytes,
	).Scan(&n.ID, &n.CreatedAt)
	
	if err != nil {
		log.Printf("❌ [NotificationHub] Lỗi lưu DB: %v", err)
		return err
	}
	
	n.UserID = userID
	n.Type = nType
	n.Title = title
	n.Message = message
	n.Data = dataBytes

	// 2. Publish tới Redis (Realtime)
	if config.RedisClient != nil {
		channel := fmt.Sprintf("user_notifications:%s", userID)
		nBytes, _ := json.Marshal(n)
		
		err := config.RedisClient.Publish(context.Background(), channel, string(nBytes)).Err()
		if err != nil {
			log.Printf("⚠️ [NotificationHub] Lỗi Publish Redis: %v", err)
		} else {
			log.Printf("📡 [NotificationHub] Đã gửi thông báo realtime tới user %s (Type: %s)", userID, nType)
		}
	}
	
	return nil
}
