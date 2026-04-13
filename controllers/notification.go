package controllers

import (
	"fmt"
	"log"
	"mindex-backend/config"
	"mindex-backend/models"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// StreamNotifications mở luồng SSE để gửi thông báo realtime cho người dùng
func StreamNotifications(c *gin.Context) {
	userID := c.GetString("user_id")
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	if config.RedisClient == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Redis not connected"})
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	channel := fmt.Sprintf("user_notifications:%s", userID)
	pubsub := config.RedisClient.Subscribe(config.Ctx, channel)
	defer pubsub.Close()

	ch := pubsub.Channel()
	
	// Gửi event "ping" định kỳ để giữ kết nối
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	log.Printf("📡 [SSE] User %s connected to Notification Stream", userID)

	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(c.Writer, "event: notification\ndata: %s\n\n", msg.Payload)
			c.Writer.Flush()
		case <-ticker.C:
			fmt.Fprintf(c.Writer, "event: ping\ndata: %d\n\n", time.Now().Unix())
			c.Writer.Flush()
		case <-c.Request.Context().Done():
			log.Printf("🔌 [SSE] User %s disconnected from Notification Stream", userID)
			return
		}
	}
}

// GetNotifications lấy lịch sử thông báo của người dùng
func GetNotifications(c *gin.Context) {
	userID := c.GetString("user_id")
	
	rows, err := config.DB.Query(config.Ctx, `
		SELECT id, user_id, type, title, message, data, read_at, created_at
		FROM notifications
		WHERE user_id = $1
		ORDER BY created_at DESC
		LIMIT 20`, userID)
	
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch notifications"})
		return
	}
	defer rows.Close()

	notifications := []models.Notification{}
	for rows.Next() {
		var n models.Notification
		rows.Scan(&n.ID, &n.UserID, &n.Type, &n.Title, &n.Message, &n.Data, &n.ReadAt, &n.CreatedAt)
		notifications = append(notifications, n)
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    notifications,
	})
}

// MarkNotificationRead đánh dấu thông báo đã đọc
func MarkNotificationRead(c *gin.Context) {
	userID := c.GetString("user_id")
	notifID := c.Param("id")

	if notifID == "all" {
		_, err := config.DB.Exec(config.Ctx, `
			UPDATE notifications SET read_at = NOW() 
			WHERE user_id = $1 AND read_at IS NULL`, userID)
		if err != nil {
			c.JSON(500, gin.H{"error": "Failed to mark all as read"})
			return
		}
	} else {
		_, err := config.DB.Exec(config.Ctx, `
			UPDATE notifications SET read_at = NOW() 
			WHERE id = $1 AND user_id = $2`, notifID, userID)
		if err != nil {
			c.JSON(500, gin.H{"error": "Failed to mark as read"})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}

// DeleteNotification xóa thông báo cụ thể hoặc tất cả
func DeleteNotification(c *gin.Context) {
	userID := c.GetString("user_id")
	notifID := c.Param("id")

	if notifID == "all" {
		_, err := config.DB.Exec(config.Ctx, `
			DELETE FROM notifications 
			WHERE user_id = $1`, userID)
		if err != nil {
			c.JSON(500, gin.H{"error": "Failed to delete all notifications"})
			return
		}
	} else {
		_, err := config.DB.Exec(config.Ctx, `
			DELETE FROM notifications 
			WHERE id = $1 AND user_id = $2`, notifID, userID)
		if err != nil {
			c.JSON(500, gin.H{"error": "Failed to delete notification"})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}

