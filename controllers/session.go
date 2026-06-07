package controllers

import (
	"encoding/json"
	"log"
	"mindex-backend/config"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// RenameSession đổi tên session
// PATCH /chat/sessions/:session_id
func RenameSession(c *gin.Context) {
	sessionID := c.Param("session_id")
	userID := c.GetString("user_id")

	var req struct {
		Title string `json:"title" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "message": "Thiếu title"})
		return
	}

	res, err := config.DB.Exec(config.Ctx,
		`UPDATE chat_histories SET title = $1 WHERE session_id = $2 AND user_id = $3`,
		req.Title, sessionID, userID)
	if err != nil || res.RowsAffected() == 0 {
		c.JSON(404, gin.H{"success": false, "message": "Không tìm thấy session"})
		return
	}
	c.JSON(200, gin.H{"success": true, "message": "Đã đổi tên session"})
}

// DeleteSession xóa session và toàn bộ lịch sử chat
// DELETE /chat/sessions/:session_id
func DeleteSession(c *gin.Context) {
	sessionID := c.Param("session_id")
	userID := c.GetString("user_id")

	res, err := config.DB.Exec(config.Ctx,
		`DELETE FROM chat_histories WHERE session_id = $1 AND user_id = $2`,
		sessionID, userID)
	if err != nil || res.RowsAffected() == 0 {
		c.JSON(404, gin.H{"success": false, "message": "Không tìm thấy session"})
		return
	}
	c.JSON(200, gin.H{"success": true, "message": "Đã xóa session"})
}

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

// GetSessionMessages lấy lịch sử tin nhắn của một session.
// Hỗ trợ pagination: ?limit=30&skip=0
//   - limit: số tin cần lấy (mặc định 0 = toàn bộ, backward-compat)
//   - skip: bỏ qua N tin từ cuối (dùng khi load thêm tin cũ hơn)
// Response thêm: total (tổng số tin), has_more (còn tin cũ hơn không)
func GetSessionMessages(c *gin.Context) {
	sessionID := c.Param("session_id")
	if sessionID == "" {
		c.JSON(400, gin.H{"error": "MISSING_SESSION_ID"})
		return
	}

	limit := parsePageInt(c.Query("limit"), 0, 0, 80)   // 0 = không paginate
	skip := parsePageInt(c.Query("skip"), 0, 0, 10000)

	var fullMessages []byte
	err := config.DB.QueryRow(config.Ctx, `
		SELECT full_messages FROM chat_histories
		WHERE session_id = $1`, sessionID).Scan(&fullMessages)

	if err != nil {
		c.JSON(200, gin.H{
			"success": true,
			"data": gin.H{
				"session_id": sessionID,
				"messages":   []interface{}{},
				"total":      0,
				"has_more":   false,
			},
		})
		return
	}

	var allMessages []map[string]interface{}
	if len(fullMessages) > 0 {
		json.Unmarshal(fullMessages, &allMessages)
	}

	// Hydrate log_id cho tất cả messages trước khi paginate
	userID := c.GetString("user_id")
	allMessages = hydrateLogIDs(allMessages, sessionID, userID)

	total := len(allMessages)
	var paged []map[string]interface{}
	hasMore := false

	if limit > 0 {
		// Lấy [total-limit-skip : total-skip] (tin gần nhất)
		end := total - skip
		if end < 0 {
			end = 0
		}
		start := end - limit
		if start < 0 {
			start = 0
		}
		paged = allMessages[start:end]
		hasMore = start > 0
	} else {
		// Không paginate: trả tất cả (backward-compat)
		paged = allMessages
		hasMore = false
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"session_id": sessionID,
			"messages":   paged,
			"total":      total,
			"has_more":   hasMore,
		},
	})
}

// hydrateLogIDs đảm bảo mỗi assistant message có log_id, ghi lại DB nếu thiếu.
func hydrateLogIDs(messages []map[string]interface{}, sessionID, userID string) []map[string]interface{} {
	changed := false
	for i, msg := range messages {
		role, ok := msg["role"].(string)
		if !ok || role != "assistant" {
			continue
		}
		if _, has := msg["log_id"]; has {
			continue
		}
		content, _ := msg["content"].(string)
		var logID string
		err := config.DB.QueryRow(config.Ctx, `
			SELECT id FROM ai_response_logs
			WHERE session_id = $1 AND answer = $2 LIMIT 1`, sessionID, content).Scan(&logID)
		if err != nil {
			logID = uuid.New().String()
			_, insertErr := config.DB.Exec(config.Ctx, `
				INSERT INTO ai_response_logs
				  (id, session_id, user_id, question, answer, model_used, latency_ms, token_count, sources_count)
				VALUES ($1, $2, $3, 'Lịch sử chat', $4, 'legacy', 0, 0, 0)`,
				logID, sessionID, userID, content)
			if insertErr != nil {
				log.Printf("❌ [LegacyLog] Failed to insert legacy log: %v", insertErr)
				continue
			}
		}
		messages[i]["log_id"] = logID
		changed = true
	}
	if changed {
		if newBytes, err := json.Marshal(messages); err == nil {
			_, updateErr := config.DB.Exec(config.Ctx, `
				UPDATE chat_histories SET full_messages = $1 WHERE session_id = $2`, newBytes, sessionID)
			if updateErr != nil {
				log.Printf("❌ [LegacyLog] Failed to update chat history: %v", updateErr)
			}
		}
	}
	return messages
}

// parsePageInt parse query param thành int với default và clamp.
func parsePageInt(s string, def, min, max int) int {
	if s == "" {
		return def
	}
	v := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return def
		}
		v = v*10 + int(ch-'0')
	}
	if v < min {
		return min
	}
	if max > 0 && v > max {
		return max
	}
	return v
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
