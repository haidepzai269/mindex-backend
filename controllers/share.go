package controllers

import (
	"encoding/json"
	"fmt"
	"log"
	"mindex-backend/config"
	"mindex-backend/utils"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// -------------------------------------------------------
// Request / Response types
// -------------------------------------------------------

type CreateSharedLinkRequest struct {
	SessionID   string `json:"session_id" binding:"required"`
	ShowHistory bool   `json:"show_history"`
	AllowFork   bool   `json:"allow_fork"`
}

// -------------------------------------------------------
// POST /share/create — Tạo Shared Link cho một session
// -------------------------------------------------------

func CreateSharedLink(c *gin.Context) {
	docID := c.Param("id")
	userID := c.GetString("user_id")

	var req CreateSharedLinkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": "VALIDATION_ERROR", "message": "session_id là bắt buộc"})
		return
	}

	// Xác minh session thuộc về user và document này
	var exists bool
	err := config.DB.QueryRow(config.Ctx, `
		SELECT EXISTS(SELECT 1 FROM chat_histories WHERE session_id = $1 AND document_id = $2 AND user_id = $3)`,
		req.SessionID, docID, userID).Scan(&exists)
	if err != nil || !exists {
		c.JSON(403, gin.H{"success": false, "error": "SESSION_NOT_FOUND", "message": "Session không hợp lệ hoặc không thuộc về bạn"})
		return
	}

	// Lấy lịch sử messages để tóm tắt
	var fullMsgBytes []byte
	_ = config.DB.QueryRow(config.Ctx, `
		SELECT full_messages FROM chat_histories WHERE session_id = $1`, req.SessionID).Scan(&fullMsgBytes)

	var messages []map[string]interface{}
	if len(fullMsgBytes) > 0 {
		json.Unmarshal(fullMsgBytes, &messages)
	}

	// Gọi LLM tóm tắt hội thoại (SYS-011 - chạy ngầm, không block response)
	var summary string
	if len(messages) > 0 {
		convText := buildConversationText(messages)
		summaryPrompt := fmt.Sprintf(`Bạn là trợ lý học thuật. Hãy tóm tắt ngắn gọn hội thoại dưới đây thành 3-5 câu bằng tiếng Việt,
nêu bật: (1) Nội dung tài liệu chính, (2) Các câu hỏi đã được hỏi, (3) Những điểm quan trọng đã được giải đáp.
Chỉ trả về đoạn tóm tắt, không kèm tiêu đề hay chú thích.

HỘI THOẠI:
%s`, convText)

		summaryMessages := []utils.ChatMessage{
			{Role: "system", Content: "Bạn là trợ lý tóm tắt hội thoại học thuật chuyên nghiệp."},
			{Role: "user", Content: summaryPrompt},
		}
		sum, _, err := utils.AI.ChatNonStream(utils.ServiceSummary, summaryMessages)
		if err != nil {
			log.Printf("⚠️ [SHARE] Không thể tóm tắt session %s: %v", req.SessionID, err)
		} else {
			summary = sum
		}
	}

	// Lấy expired_at từ document gốc
	var expiredAt *time.Time
	_ = config.DB.QueryRow(config.Ctx, `SELECT expired_at FROM documents WHERE id = $1`, docID).Scan(&expiredAt)

	// Lưu shared_link
	settings := map[string]bool{
		"show_history": req.ShowHistory,
		"allow_fork":   req.AllowFork,
	}
	settingsBytes, _ := json.Marshal(settings)

	linkID := uuid.New().String()
	var summaryPtr *string
	if summary != "" {
		summaryPtr = &summary
	}

	_, err = config.DB.Exec(config.Ctx, `
		INSERT INTO shared_links (id, document_id, session_id, creator_id, settings, summary, expired_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		linkID, docID, req.SessionID, userID, string(settingsBytes), summaryPtr, expiredAt)

	if err != nil {
		log.Printf("❌ [SHARE] Lỗi lưu shared_link: %v", err)
		c.JSON(500, gin.H{"success": false, "error": "DB_ERROR", "message": "Không thể tạo link chia sẻ"})
		return
	}

	// Cập nhật shared_at trong bảng documents để phục vụ sắp xếp (vừa chia sẻ sẽ lên đầu)
	_, _ = config.DB.Exec(config.Ctx, `UPDATE documents SET shared_at = NOW() WHERE id = $1`, docID)

	log.Printf("✅ [SHARE] Đã tạo shared_link %s cho session %s của doc %s", linkID, req.SessionID, docID)

	c.JSON(201, gin.H{
		"success": true,
		"data": gin.H{
			"link_id":   linkID,
			"share_url": fmt.Sprintf("/shared/%s", linkID),
		},
	})
}

// -------------------------------------------------------
// GET /public/shared/:link_id — Xem Shared Link (Public)
// -------------------------------------------------------

func GetSharedLink(c *gin.Context) {
	linkID := c.Param("link_id")

	// Lấy thông tin shared_link
	var docID, sessionID, creatorID string
	var settingsJSON []byte
	var summary *string
	var expiredAt *time.Time
	var createdAt time.Time

	err := config.DB.QueryRow(config.Ctx, `
		SELECT document_id, session_id, creator_id, settings, summary, expired_at, created_at
		FROM shared_links WHERE id = $1`, linkID).Scan(
		&docID, &sessionID, &creatorID, &settingsJSON, &summary, &expiredAt, &createdAt)

	if err != nil {
		c.JSON(404, gin.H{"success": false, "error": "LINK_NOT_FOUND", "message": "Link chia sẻ không tồn tại hoặc đã hết hạn"})
		return
	}

	// Kiểm tra hết hạn
	if expiredAt != nil && time.Now().After(*expiredAt) {
		c.JSON(410, gin.H{"success": false, "error": "LINK_EXPIRED", "message": "Link chia sẻ đã hết hạn"})
		return
	}

	// Parse settings
	var settings map[string]bool
	json.Unmarshal(settingsJSON, &settings)

	// Lấy thông tin document
	var docTitle, docStatus string
	_ = config.DB.QueryRow(config.Ctx, `SELECT title, status FROM documents WHERE id = $1`, docID).Scan(&docTitle, &docStatus)

	// Lấy thông tin creator (display_name)
	var creatorName string
	_ = config.DB.QueryRow(config.Ctx, `SELECT display_name FROM users WHERE id = $1`, creatorID).Scan(&creatorName)

	// Lấy messages nếu show_history = true
	var fullMessages []interface{}
	if settings["show_history"] {
		var fullMsgBytes []byte
		_ = config.DB.QueryRow(config.Ctx, `
			SELECT full_messages FROM chat_histories WHERE session_id = $1`, sessionID).Scan(&fullMsgBytes)
		if len(fullMsgBytes) > 0 {
			json.Unmarshal(fullMsgBytes, &fullMessages)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"link_id":      linkID,
			"document_id":  docID,
			"session_id":   sessionID,
			"document":     gin.H{"id": docID, "title": docTitle, "status": docStatus},
			"creator":      gin.H{"display_name": creatorName},
			"settings":     settings,
			"summary":      summary,
			"messages":     fullMessages,
			"created_at":   createdAt,
			"expired_at":   expiredAt,
		},
	})
}

// -------------------------------------------------------
// Helper: Chuyển messages array thành text hội thoại
// -------------------------------------------------------

func buildConversationText(messages []map[string]interface{}) string {
	var text string
	for _, msg := range messages {
		role, _ := msg["role"].(string)
		content, _ := msg["content"].(string)
		if role == "user" {
			text += fmt.Sprintf("Người dùng: %s\n", content)
		} else if role == "assistant" {
			text += fmt.Sprintf("Trợ lý: %s\n\n", content)
		}
	}
	return text
}
