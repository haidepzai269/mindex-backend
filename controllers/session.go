package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"mindex-backend/config"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	normalChatScope          = "document"
	normalChatRedisTTL       = time.Hour
	normalChatRedisPairLimit = 10
	softDeletedAtField       = "deleted_at"
)

var errSessionMessageNotFound = errors.New("session message not found")

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
//
// Response thêm: total (tổng số tin), has_more (còn tin cũ hơn không)
func GetSessionMessages(c *gin.Context) {
	sessionID := c.Param("session_id")
	userID := c.GetString("user_id")
	if sessionID == "" {
		c.JSON(400, gin.H{"error": "MISSING_SESSION_ID"})
		return
	}

	limit := parsePageInt(c.Query("limit"), 0, 0, 80) // 0 = không paginate
	skip := parsePageInt(c.Query("skip"), 0, 0, 10000)

	var fullMessages []byte
	err := config.DB.QueryRow(config.Ctx, `
		SELECT full_messages FROM chat_histories
		WHERE session_id = $1 AND user_id = $2 AND chat_scope = $3`,
		sessionID, userID, normalChatScope).Scan(&fullMessages)

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
		if err := json.Unmarshal(fullMessages, &allMessages); err != nil {
			log.Printf("❌ [Session] Failed to decode session history %s: %v", sessionID, err)
			allMessages = []map[string]interface{}{}
		}
	}

	// Hydrate log_id cho tất cả assistant messages active trước khi paginate
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
			"messages":   serializeSessionMessages(paged),
			"total":      total,
			"has_more":   hasMore,
		},
	})
}

// DeleteMessage xóa mềm một tin nhắn trong session thường của user.
// DELETE /chat/sessions/:session_id/messages/:message_id
func DeleteMessage(c *gin.Context) {
	mutateSessionMessageState(c, false)
}

// RestoreMessage khôi phục một tin nhắn đã soft delete trong session thường của user.
// POST /chat/sessions/:session_id/messages/:message_id/restore
func RestoreMessage(c *gin.Context) {
	mutateSessionMessageState(c, true)
}

func mutateSessionMessageState(c *gin.Context, restore bool) {
	sessionID := c.Param("session_id")
	messageID := c.Param("message_id")
	userID := c.GetString("user_id")
	if sessionID == "" || messageID == "" {
		c.JSON(400, gin.H{"success": false, "error": "MISSING_MESSAGE_ID", "message": "Thiếu session_id hoặc message_id"})
		return
	}

	message, err := updateNormalChatMessageState(c.Request.Context(), sessionID, userID, messageID, restore)
	if err != nil {
		switch {
		case errors.Is(err, pgx.ErrNoRows), errors.Is(err, errSessionMessageNotFound):
			c.JSON(404, gin.H{"success": false, "message": "Không tìm thấy tin nhắn"})
		default:
			log.Printf("❌ [Session] Failed to mutate message %s in session %s: %v", messageID, sessionID, err)
			c.JSON(500, gin.H{"success": false, "message": "Không thể cập nhật tin nhắn"})
		}
		return
	}

	successMessage := "Đã xóa tin nhắn"
	if restore {
		successMessage = "Đã khôi phục tin nhắn"
	}

	c.JSON(200, gin.H{
		"success": true,
		"message": successMessage,
		"data": gin.H{
			"session_id": sessionID,
			"message":    message,
		},
	})
}

func updateNormalChatMessageState(ctx context.Context, sessionID, userID, messageID string, restore bool) (map[string]interface{}, error) {
	tx, err := config.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	messages, err := loadNormalChatMessagesForUpdate(ctx, tx, sessionID, userID)
	if err != nil {
		return nil, err
	}

	messageIndex := findSessionMessageIndex(messages, messageID)
	if messageIndex < 0 {
		return nil, errSessionMessageNotFound
	}

	if restore {
		delete(messages[messageIndex], softDeletedAtField)
	} else if !isSessionMessageDeleted(messages[messageIndex]) {
		messages[messageIndex][softDeletedAtField] = time.Now().UTC().Format(time.RFC3339)
	}

	newBytes, err := json.Marshal(messages)
	if err != nil {
		return nil, err
	}

	_, err = tx.Exec(ctx, `
		UPDATE chat_histories
		SET full_messages = $1
		WHERE session_id = $2 AND user_id = $3 AND chat_scope = $4`,
		newBytes, sessionID, userID, normalChatScope)
	if err != nil {
		return nil, err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE shared_links
		SET expired_at = NOW()
		WHERE session_id = $1 AND (expired_at IS NULL OR expired_at > NOW())`,
		sessionID); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	rebuildNormalChatRedisHistory(sessionID, messages)
	return serializeSessionMessage(messages[messageIndex]), nil
}

func loadNormalChatMessagesForUpdate(ctx context.Context, tx pgx.Tx, sessionID, userID string) ([]map[string]interface{}, error) {
	var fullMessages []byte
	err := tx.QueryRow(ctx, `
		SELECT full_messages FROM chat_histories
		WHERE session_id = $1 AND user_id = $2 AND chat_scope = $3
		FOR UPDATE`,
		sessionID, userID, normalChatScope).Scan(&fullMessages)
	if err != nil {
		return nil, err
	}

	var messages []map[string]interface{}
	if len(fullMessages) == 0 {
		return messages, nil
	}
	if err := json.Unmarshal(fullMessages, &messages); err != nil {
		return nil, err
	}
	return messages, nil
}

func findSessionMessageIndex(messages []map[string]interface{}, messageID string) int {
	for i, msg := range messages {
		if id, _ := msg["id"].(string); id == messageID {
			return i
		}
	}
	return -1
}

func rebuildNormalChatRedisHistory(sessionID string, messages []map[string]interface{}) {
	if config.RedisClient == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	historyKey := "session:" + sessionID
	if err := config.RedisClient.Del(ctx, historyKey).Err(); err != nil {
		log.Printf("⚠️ [Session] Failed to clear Redis history for %s: %v", sessionID, err)
	}

	history := buildNormalChatRedisHistory(messages)
	if len(history) == 0 {
		return
	}

	pipe := config.RedisClient.Pipeline()
	for _, qa := range history {
		qaBytes, err := json.Marshal(qa)
		if err != nil {
			log.Printf("⚠️ [Session] Failed to marshal Redis history item for %s: %v", sessionID, err)
			continue
		}
		pipe.RPush(ctx, historyKey, string(qaBytes))
	}
	pipe.LTrim(ctx, historyKey, -normalChatRedisPairLimit, -1)
	pipe.Expire(ctx, historyKey, normalChatRedisTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		log.Printf("⚠️ [Session] Failed to rebuild Redis history for %s: %v", sessionID, err)
		if delErr := config.RedisClient.Del(ctx, historyKey).Err(); delErr != nil {
			log.Printf("⚠️ [Session] Failed to clear stale Redis history for %s: %v", sessionID, delErr)
		}
	}
}

// buildNormalChatRedisHistory rebuild prompt history only from intact user -> assistant exchanges.
func buildNormalChatRedisHistory(messages []map[string]interface{}) []QAHistory {
	history := make([]QAHistory, 0, normalChatRedisPairLimit)
	pendingQuestion := ""

	for _, msg := range messages {
		role, _ := msg["role"].(string)
		if role == "" {
			continue
		}

		if isSessionMessageDeleted(msg) {
			if role == "user" || role == "assistant" {
				pendingQuestion = ""
			}
			continue
		}

		content, _ := msg["content"].(string)
		if content == "" {
			continue
		}

		switch role {
		case "user":
			pendingQuestion = sessionMessageQuestionForHistory(msg, content)
		case "assistant":
			if pendingQuestion == "" {
				continue
			}
			history = append(history, QAHistory{Question: pendingQuestion, Answer: content})
			pendingQuestion = ""
		}
	}

	if len(history) > normalChatRedisPairLimit {
		history = history[len(history)-normalChatRedisPairLimit:]
	}
	return history
}

func sessionMessageQuestionForHistory(msg map[string]interface{}, content string) string {
	rawAttachments, ok := msg["attachments"].([]interface{})
	if !ok || len(rawAttachments) == 0 {
		return content
	}

	previews := make([]string, 0, len(rawAttachments))
	for i, raw := range rawAttachments {
		att, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		preview, _ := att["ocr_text"].(string)
		if strings.TrimSpace(preview) == "" {
			preview, _ = att["ocr_preview"].(string)
		}
		preview = strings.TrimSpace(preview)
		if preview == "" {
			preview = "(no OCR text)"
		}
		previews = append(previews, compactChatPreview("Image "+strconv.Itoa(i+1)+" OCR: "+preview, 500))
	}
	if len(previews) == 0 {
		return content
	}
	return strings.TrimSpace(content + "\n" + strings.Join(previews, "\n"))
}

func serializeSessionMessages(messages []map[string]interface{}) []map[string]interface{} {
	serialized := make([]map[string]interface{}, 0, len(messages))
	for _, msg := range messages {
		serialized = append(serialized, serializeSessionMessage(msg))
	}
	return serialized
}

func serializeSessionMessage(msg map[string]interface{}) map[string]interface{} {
	if isSessionMessageDeleted(msg) {
		tombstone := gin.H{
			"is_deleted": true,
		}
		if id, ok := msg["id"].(string); ok {
			tombstone["id"] = id
		}
		if role, ok := msg["role"].(string); ok {
			tombstone["role"] = role
		}
		if timestamp, ok := msg["timestamp"].(string); ok {
			tombstone["timestamp"] = timestamp
		}
		if deletedAt, ok := msg[softDeletedAtField].(string); ok {
			tombstone["deleted_at"] = deletedAt
		}
		return tombstone
	}

	active := make(map[string]interface{}, len(msg)+1)
	for key, value := range msg {
		if key == softDeletedAtField {
			continue
		}
		active[key] = value
	}
	active["is_deleted"] = false
	return active
}

func isSessionMessageDeleted(msg map[string]interface{}) bool {
	deletedAt, ok := msg[softDeletedAtField].(string)
	return ok && deletedAt != ""
}

// hydrateLogIDs đảm bảo mỗi assistant message active có log_id, ghi lại DB nếu thiếu.
func hydrateLogIDs(messages []map[string]interface{}, sessionID, userID string) []map[string]interface{} {
	changed := false
	for i, msg := range messages {
		role, ok := msg["role"].(string)
		if !ok || role != "assistant" || isSessionMessageDeleted(msg) {
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
				UPDATE chat_histories
				SET full_messages = $1
				WHERE session_id = $2 AND user_id = $3 AND chat_scope = $4`,
				newBytes, sessionID, userID, normalChatScope)
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
