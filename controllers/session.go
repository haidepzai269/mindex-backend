package controllers

import (
	"encoding/json"
	"mindex-backend/config"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func CreateSession(c *gin.Context) {
	var req struct {
		DocumentID string `json:"document_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "VALIDATION_ERROR", "message": "Yêu cầu document_id"})
		return
	}

	userID := c.GetString("user_id")
	sessionID := uuid.New().String()

	// Khởi tạo session vào PostgreSQL history
	_, err := config.DB.Exec(config.Ctx, `
		INSERT INTO chat_histories (user_id, document_id, session_id, full_messages) 
		VALUES ($1, $2, $3, '[]'::jsonb)`, userID, req.DocumentID, sessionID)

	if err != nil {
		c.JSON(500, gin.H{"error": "INTERNAL_ERROR", "message": "Không thể tạo session"})
		return
	}

	c.JSON(201, gin.H{
		"data": gin.H{
			"session_id":  sessionID,
			"document_id": req.DocumentID,
		},
	})
}

// GetSessionMessages lấy lịch sử tin nhắn của một session
func GetSessionMessages(c *gin.Context) {
	sessionID := c.Param("session_id")
	if sessionID == "" {
		c.JSON(400, gin.H{"error": "MISSING_SESSION_ID"})
		return
	}

	var fullMessages []byte
	err := config.DB.QueryRow(config.Ctx, `
		SELECT full_messages FROM chat_histories 
		WHERE session_id = $1`, sessionID).Scan(&fullMessages)

	if err != nil {
		// Trả về mảng rỗng thay vì 404 để frontend không bị lỗi
		c.JSON(200, gin.H{
			"success": true,
			"data": gin.H{
				"session_id": sessionID,
				"messages":   []interface{}{},
			},
		})
		return
	}

	var messages []interface{}
	if len(fullMessages) > 0 {
		json.Unmarshal(fullMessages, &messages)
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"session_id": sessionID,
			"messages":   messages,
		},
	})
}

// GetActiveSession tìm session gần nhất của user với tài liệu này
func GetActiveSession(c *gin.Context) {
	docID := c.Param("doc_id")
	userID := c.GetString("user_id")

	var sessionID string
	err := config.DB.QueryRow(config.Ctx, `
		SELECT session_id FROM chat_histories 
		WHERE user_id = $1 AND document_id = $2 AND message_count > 0
		ORDER BY started_at DESC LIMIT 1`, userID, docID).Scan(&sessionID)

	if err != nil {
		c.JSON(200, gin.H{"success": true, "data": nil}) // Không có session cũ cũng không sao
		return
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"session_id": sessionID,
		},
	})
}
