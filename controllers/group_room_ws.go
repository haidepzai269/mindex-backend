package controllers

import (
	"encoding/json"
	"fmt"
	"log"
	"mindex-backend/config"
	"mindex-backend/internal/ws"
	"mindex-backend/models"
	"mindex-backend/utils"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	gorillaws "github.com/gorilla/websocket"
)

var roomWsUpgrader = gorillaws.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return config.IsAllowedOrigin(r.Header.Get("Origin"))
	},
}

// ConnectRoomWS — GET /api/v1/rooms/:id/ws
func ConnectRoomWS(c *gin.Context) {
	userID := c.GetString("user_id")
	roomID := c.Param("id")

	// Kiểm tra user là thành viên active
	if !IsRoomMember(roomID, userID) {
		c.JSON(403, gin.H{"success": false, "message": "Không có quyền vào phòng"})
		return
	}

	conn, err := roomWsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("Room WS Upgrade Error: %v", err)
		return
	}

	client := &ws.RoomClient{
		Hub:    ws.RoomHubInstance,
		Conn:   conn,
		UserID: userID,
		RoomID: roomID,
		Send:   make(chan []byte, 128),
	}

	ws.RoomHubInstance.Register <- client
	if config.RedisClient != nil {
		config.RedisClient.Set(config.Ctx, fmt.Sprintf("room_online:%s:%s", roomID, userID), "1", 120*time.Second)
		config.RedisClient.Set(config.Ctx, fmt.Sprintf("room_last_seen:%s:%s", roomID, userID), time.Now().Format(time.RFC3339), 7*24*time.Hour)
	}

	// Gửi history đồng bộ trước khi bắt đầu read loop để tránh race với close(client.Send)
	sendRoomHistory(client, roomID)

	// Broadcast user online
	var userName string
	config.DB.QueryRow(config.Ctx, `SELECT name FROM users WHERE id = $1`, userID).Scan(&userName)
	ws.RoomHubInstance.BroadcastToRoomExcept(roomID, userID, models.RoomEvent{
		Type: "user_online", RoomID: roomID, UserID: userID,
		Payload: gin.H{"user_id": userID, "name": userName},
	})

	go client.WritePump()
	client.ReadPump(func(msg []byte) {
		handleRoomIncomingMessage(client, roomID, userID, msg)
	})
}

// wsRateLimitOK kiểm tra rate limit cho WS message (10 msg / 10s per user per room)
func wsRateLimitOK(roomID, userID string) bool {
	if config.RedisClient == nil {
		return true
	}
	key := fmt.Sprintf("ws_rate:%s:%s", roomID, userID)
	count, _ := config.RedisClient.Incr(config.Ctx, key).Result()
	if count == 1 {
		config.RedisClient.Expire(config.Ctx, key, 10*time.Second)
	}
	return count <= 10
}

// handleRoomIncomingMessage xử lý tin nhắn đến từ client WS
func handleRoomIncomingMessage(client *ws.RoomClient, roomID, userID string, raw []byte) {
	var incoming struct {
		Type      string `json:"type"`
		Text      string `json:"text"`
		ReplyToID string `json:"reply_to_id,omitempty"`
	}
	if err := json.Unmarshal(raw, &incoming); err != nil {
		return
	}

	switch incoming.Type {
	case "ping":
		// Heartbeat: SET room_online:{roomID}:{userID} với TTL 30s
		if config.RedisClient != nil {
			config.RedisClient.Set(config.Ctx,
				fmt.Sprintf("room_online:%s:%s", roomID, userID),
				"1", 120*time.Second) // 2 phút grace period
			config.RedisClient.Set(config.Ctx,
				fmt.Sprintf("room_last_seen:%s:%s", roomID, userID),
				time.Now().Format(time.RFC3339), 7*24*time.Hour)
		}

	case "chat_message":
		if strings.TrimSpace(incoming.Text) == "" {
			return
		}

		// Recheck membership — user có thể đã bị kick hoặc đã leave
		if !IsRoomMember(roomID, userID) {
			return
		}

		// Rate limit WS messages
		if !wsRateLimitOK(roomID, userID) {
			return
		}

		var userName string
		config.DB.QueryRow(config.Ctx, `SELECT name FROM users WHERE id = $1`, userID).Scan(&userName)

		parsed := parseRoomMessage(incoming.Text, roomID)

		msg := models.RoomChatMessage{
			ID:         fmt.Sprintf("%d", time.Now().UnixNano()),
			RoomID:     roomID,
			UserID:     userID,
			UserName:   userName,
			Text:       parsed.RawText,
			MentionsAI: parsed.MentionsAI,
			Mentions:   parsed.MentionedUIDs,
			ReplyToID:  incoming.ReplyToID,
			Timestamp:  time.Now(),
		}

		// Broadcast tin nhắn tới tất cả trong phòng
		ws.RoomHubInstance.BroadcastToRoom(roomID, models.RoomEvent{
			Type: "chat_message", RoomID: roomID, UserID: userID,
			Payload: msg,
		})

		// Lưu vào PostgreSQL (bền vững)
		config.DB.Exec(config.Ctx, `
			INSERT INTO room_messages (id, room_id, user_id, user_name, text, reply_to_id, mentions_ai, timestamp)
			VALUES ($1, $2, $3, $4, $5, NULLIF($6,''), $7, $8)
			ON CONFLICT (id) DO NOTHING`,
			msg.ID, roomID, userID, userName, msg.Text, msg.ReplyToID, msg.MentionsAI, msg.Timestamp)

		// Lưu vào Redis cache (LPUSH + LTRIM giữ 50 tin cho performance)
		if config.RedisClient != nil {
			msgJSON, _ := json.Marshal(msg)
			key := fmt.Sprintf("room_chat_history:%s", roomID)
			config.RedisClient.LPush(config.Ctx, key, msgJSON)
			config.RedisClient.LTrim(config.Ctx, key, 0, 49)
		}

		// Nếu @MindexAI → trigger AI (Sprint 4)
		if parsed.MentionsAI {
			go handleRoomAI(roomID, userID, parsed.CleanForRAG)
		}

	case "typing":
		var userName string
		config.DB.QueryRow(config.Ctx, `SELECT name FROM users WHERE id = $1`, userID).Scan(&userName)
		ws.RoomHubInstance.BroadcastToRoomExcept(roomID, userID, models.RoomEvent{
			Type: "user_typing", RoomID: roomID, UserID: userID,
			Payload: gin.H{"user_id": userID, "name": userName},
		})

	case "message_reaction":
		// Recheck membership
		if !IsRoomMember(roomID, userID) {
			return
		}

		var reactReq struct {
			MessageID string `json:"message_id"`
			Emoji     string `json:"emoji"`
		}
		if err := json.Unmarshal(raw, &reactReq); err != nil {
			return
		}
		handleMessageReaction(roomID, userID, reactReq.MessageID, reactReq.Emoji)
	}
}

func handleMessageReaction(roomID, userID, msgID, emoji string) {
	if config.RedisClient == nil {
		return
	}
	key := fmt.Sprintf("room_chat_history:%s", roomID)
	msgs, err := config.RedisClient.LRange(config.Ctx, key, 0, 49).Result()
	if err != nil {
		return
	}

	var updatedMsg *models.RoomChatMessage
	for i, mJSON := range msgs {
		var m models.RoomChatMessage
		if json.Unmarshal([]byte(mJSON), &m) == nil && m.ID == msgID {
			if m.Reactions == nil {
				m.Reactions = make(map[string][]string)
			}

			// Toggle reaction: Nếu đã thả cùng emoji thì xóa, chưa thì thêm
			uids := m.Reactions[emoji]
			found := false
			for idx, uid := range uids {
				if uid == userID {
					m.Reactions[emoji] = append(uids[:idx], uids[idx+1:]...)
					found = true
					break
				}
			}
			if !found {
				m.Reactions[emoji] = append(uids, userID)
			}

			if len(m.Reactions[emoji]) == 0 {
				delete(m.Reactions, emoji)
			}

			updatedMsg = &m
			newJSON, _ := json.Marshal(m)
			config.RedisClient.LSet(config.Ctx, key, int64(i), newJSON)
			break
		}
	}

	if updatedMsg != nil {
		ws.RoomHubInstance.BroadcastToRoom(roomID, models.RoomEvent{
			Type: "message_reaction", RoomID: roomID, UserID: userID,
			Payload: updatedMsg,
		})
	}
}

// sendRoomHistory gửi 50 tin nhắn gần nhất khi user vào phòng
// Ưu tiên Redis cache, fallback về PostgreSQL nếu cache trống
func sendRoomHistory(client *ws.RoomClient, roomID string) {
	var history []json.RawMessage

	// 1. Thử Redis cache trước
	if config.RedisClient != nil {
		key := fmt.Sprintf("room_chat_history:%s", roomID)
		msgs, err := config.RedisClient.LRange(config.Ctx, key, 0, 49).Result()
		if err == nil && len(msgs) > 0 {
			// Đảo ngược cũ → mới
			for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
				msgs[i], msgs[j] = msgs[j], msgs[i]
			}
			for _, m := range msgs {
				history = append(history, json.RawMessage(m))
			}
		}
	}

	// 2. Fallback: load từ PostgreSQL nếu Redis trống
	if len(history) == 0 {
		rows, err := config.DB.Query(config.Ctx, `
			SELECT id, room_id, user_id, user_name, text,
			       COALESCE(reply_to_id, ''), mentions_ai, timestamp
			FROM room_messages
			WHERE room_id = $1
			ORDER BY timestamp DESC LIMIT 50`, roomID)
		if err == nil {
			defer rows.Close()
			var dbMsgs []models.RoomChatMessage
			for rows.Next() {
				var m models.RoomChatMessage
				rows.Scan(&m.ID, &m.RoomID, &m.UserID, &m.UserName, &m.Text,
					&m.ReplyToID, &m.MentionsAI, &m.Timestamp)
				dbMsgs = append(dbMsgs, m)
			}
			// Đảo ngược: DB trả DESC, cần ASC
			for i, j := 0, len(dbMsgs)-1; i < j; i, j = i+1, j-1 {
				dbMsgs[i], dbMsgs[j] = dbMsgs[j], dbMsgs[i]
			}
			for _, m := range dbMsgs {
				b, _ := json.Marshal(m)
				history = append(history, json.RawMessage(b))
			}

			// Warm up Redis cache
			if config.RedisClient != nil && len(dbMsgs) > 0 {
				key := fmt.Sprintf("room_chat_history:%s", roomID)
				for i := len(dbMsgs) - 1; i >= 0; i-- {
					b, _ := json.Marshal(dbMsgs[i])
					config.RedisClient.LPush(config.Ctx, key, b)
				}
				config.RedisClient.LTrim(config.Ctx, key, 0, 49)
			}
		}
	}

	if len(history) == 0 {
		return
	}

	payload, _ := json.Marshal(models.RoomEvent{
		Type:    "history",
		RoomID:  roomID,
		Payload: history,
	})
	select {
	case client.Send <- payload:
	default:
	}
}

// ============================================================
// Sprint 4: @mention parse & AI Integration
// ============================================================

type ParsedMessage struct {
	RawText       string
	MentionsAI    bool
	MentionedUIDs []string
	CleanForRAG   string
}

var mentionRegex = regexp.MustCompile(`@(\w+)`)

func parseRoomMessage(text, roomID string) ParsedMessage {
	result := ParsedMessage{RawText: text}

	// Load member names trong phòng
	type memberInfo struct {
		UserID string
		Name   string
	}
	var members []memberInfo
	rows, _ := config.DB.Query(config.Ctx, `
		SELECT grm.user_id, u.name FROM group_room_members grm
		JOIN users u ON grm.user_id = u.id
		WHERE grm.room_id = $1 AND grm.left_at IS NULL`, roomID)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var m memberInfo
			rows.Scan(&m.UserID, &m.Name)
			members = append(members, m)
		}
	}

	matches := mentionRegex.FindAllStringSubmatch(text, -1)
	clean := text
	for _, match := range matches {
		tag := match[1]
		if strings.EqualFold(tag, "MindexAI") {
			result.MentionsAI = true
			clean = strings.ReplaceAll(clean, match[0], "")
		} else {
			for _, m := range members {
				if strings.EqualFold(tag, strings.ReplaceAll(m.Name, " ", "")) {
					result.MentionedUIDs = append(result.MentionedUIDs, m.UserID)
					break
				}
			}
		}
	}
	result.CleanForRAG = strings.TrimSpace(clean)
	return result
}

// handleRoomAI xử lý AI response khi @MindexAI được gọi
func handleRoomAI(roomID, callerUserID, query string) {
	log.Printf("🤖 [RoomAI] Start room=%s caller=%s query=%q", roomID, callerUserID, query)

	// Redis lock để tránh nhiều AI cùng lúc
	lockKey := fmt.Sprintf("ai_lock:room:%s", roomID)
	if config.RedisClient != nil {
		ok, _ := config.RedisClient.SetNX(config.Ctx, lockKey, callerUserID, 30*time.Second).Result()
		if !ok {
			ws.RoomHubInstance.BroadcastToRoom(roomID, models.RoomEvent{
				Type: "ai_busy", RoomID: roomID,
				Payload: gin.H{"message": "MindexAI đang trả lời câu hỏi trước, vui lòng chờ."},
			})
			return
		}
		defer config.RedisClient.Del(config.Ctx, lockKey)
	}

	// Broadcast typing indicator
	ws.RoomHubInstance.BroadcastToRoom(roomID, models.RoomEvent{
		Type: "ai_typing", RoomID: roomID,
		Payload: gin.H{"message": "MindexAI đang trả lời..."},
	})

	ragContext := buildGroupRoomContext(roomID, query)

	// Short-circuit: không có tài liệu hoặc không tìm thấy nội dung liên quan → bỏ qua AI call
	if ragContext == "" || ragContext == "Chưa có tài liệu nào trong phòng." || ragContext == "Không tìm thấy nội dung liên quan trong tài liệu của phòng." {
		log.Printf("⚡ [RoomAI] No relevant context for room=%s, short-circuiting AI call (ragContext=%q)", roomID, ragContext)
		var noContextMsg string
		if ragContext == "" || ragContext == "Chưa có tài liệu nào trong phòng." {
			noContextMsg = "Phòng chưa có tài liệu nào. Hãy thêm tài liệu vào phòng để MindexAI có thể hỗ trợ học tập!"
		} else {
			noContextMsg = "Xin lỗi, tôi không tìm thấy nội dung liên quan đến câu hỏi này trong tài liệu của phòng. Hãy thử hỏi về nội dung được đề cập trong các tài liệu."
		}
		broadcastRoomAIHardcoded(roomID, noContextMsg)
		return
	}

	chatHistory := getRoomChatHistory(roomID, 10)
	log.Printf("🧠 [RoomAI] room=%s context_len=%d history_len=%d", roomID, len(ragContext), len(chatHistory))

	systemMsg := fmt.Sprintf(`Bạn là MindexAI, trợ lý học tập cho phòng học nhóm. Hãy trả lời dựa trên tài liệu bên dưới.

=== TÀI LIỆU TRONG PHÒNG ===
%s

=== LỊCH SỞ CHAT ===
%s`, ragContext, chatHistory)

	messages := []utils.ChatMessage{
		{Role: "system", Content: systemMsg},
		{Role: "user", Content: query},
	}

	answer, usedProvider, err := utils.AI.ChatNonStream(utils.ServiceChat, messages)
	if err != nil {
		log.Printf("❌ [RoomAI] room=%s provider_error=%v", roomID, err)
		ws.RoomHubInstance.BroadcastToRoom(roomID, models.RoomEvent{
			Type: "ai_error", RoomID: roomID,
			Payload: gin.H{"message": "MindexAI không phản hồi, thử lại sau."},
		})
		return
	}
	if strings.TrimSpace(answer) == "" {
		log.Printf("❌ [RoomAI] room=%s provider=%s returned empty answer after non-stream success", roomID, usedProvider)
		ws.RoomHubInstance.BroadcastToRoom(roomID, models.RoomEvent{
			Type: "ai_error", RoomID: roomID,
			Payload: gin.H{"message": "MindexAI không tạo được câu trả lời hợp lệ, thử lại sau."},
		})
		return
	}
	log.Printf("✅ [RoomAI] room=%s provider=%s answer_len=%d", roomID, usedProvider, len(answer))

	// Broadcast theo từng chunk giả lập (non-stream)
	words := strings.Fields(answer)
	for i, w := range words {
		chunk := w
		if i < len(words)-1 {
			chunk += " "
		}
		ws.RoomHubInstance.BroadcastToRoom(roomID, models.RoomEvent{
			Type: "ai_chunk", RoomID: roomID,
			Payload: gin.H{"chunk": chunk},
		})
	}

	// Lưu AI response vào history
	aiMsg := models.RoomChatMessage{
		ID:        fmt.Sprintf("ai_%d", time.Now().UnixNano()),
		RoomID:    roomID,
		UserName:  "MindexAI",
		Text:      answer,
		IsAI:      true,
		Timestamp: time.Now(),
	}
	if config.RedisClient != nil {
		msgJSON, _ := json.Marshal(aiMsg)
		key := fmt.Sprintf("room_chat_history:%s", roomID)
		config.RedisClient.LPush(config.Ctx, key, msgJSON)
		config.RedisClient.LTrim(config.Ctx, key, 0, 49)
	}

	ws.RoomHubInstance.BroadcastToRoom(roomID, models.RoomEvent{
		Type: "ai_done", RoomID: roomID,
		Payload: gin.H{"full_text": answer, "source_info": "Dựa trên tài liệu trong phòng"},
	})
}

// getRoomChatHistory lấy N tin nhắn gần nhất từ Redis
func getRoomChatHistory(roomID string, n int) string {
	if config.RedisClient == nil {
		return ""
	}
	key := fmt.Sprintf("room_chat_history:%s", roomID)
	msgs, err := config.RedisClient.LRange(config.Ctx, key, 0, int64(n-1)).Result()
	if err != nil {
		return ""
	}

	var sb strings.Builder
	for i := len(msgs) - 1; i >= 0; i-- {
		var msg models.RoomChatMessage
		if json.Unmarshal([]byte(msgs[i]), &msg) == nil {
			if msg.IsAI {
				sb.WriteString(fmt.Sprintf("MindexAI: %s\n", msg.Text))
			} else {
				sb.WriteString(fmt.Sprintf("%s: %s\n", msg.UserName, msg.Text))
			}
		}
	}
	return sb.String()
}

func broadcastRoomAIHardcoded(roomID, msg string) {
	words := strings.Fields(msg)
	for i, w := range words {
		chunk := w
		if i < len(words)-1 {
			chunk += " "
		}
		ws.RoomHubInstance.BroadcastToRoom(roomID, models.RoomEvent{
			Type: "ai_chunk", RoomID: roomID,
			Payload: gin.H{"chunk": chunk},
		})
	}
	ws.RoomHubInstance.BroadcastToRoom(roomID, models.RoomEvent{
		Type: "ai_done", RoomID: roomID,
		Payload: gin.H{"full_text": msg, "source_info": ""},
	})
}

// GetRoomHistory trả về lịch sử chat cũ hơn theo cursor-based pagination.
// GET /rooms/:id/history?before={unix_ms}&limit=30
// before: unix timestamp milliseconds của tin nhắn cũ nhất đang hiển thị (0 = lấy mới nhất)
// limit: số tin cần lấy (mặc định 30, tối đa 100)
func GetRoomHistory(c *gin.Context) {
	roomID := c.Param("id")
	userID := c.GetString("user_id")

	// Kiểm tra user có trong phòng không
	var isMember bool
	config.DB.QueryRow(config.Ctx, `
		SELECT EXISTS(SELECT 1 FROM group_room_members WHERE room_id = $1 AND user_id = $2 AND left_at IS NULL)`,
		roomID, userID).Scan(&isMember)
	if !isMember {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "error": "NOT_MEMBER"})
		return
	}

	limitStr := c.DefaultQuery("limit", "30")
	limit, _ := strconv.Atoi(limitStr)
	if limit <= 0 || limit > 100 {
		limit = 30
	}

	beforeStr := c.Query("before") // unix ms
	var rows interface{}
	var err error

	type rowMsg struct {
		ID         string    `json:"id"`
		RoomID     string    `json:"room_id"`
		UserID     *string   `json:"user_id"`
		UserName   string    `json:"user_name"`
		Text       string    `json:"text"`
		ReplyToID  *string   `json:"reply_to_id"`
		MentionsAI bool      `json:"mentions_ai"`
		IsAI       bool      `json:"is_ai"`
		Timestamp  time.Time `json:"timestamp"`
	}
	_ = rows
	_ = err

	var dbRows interface {
		Next() bool
		Close()
	}
	var queryErr error

	if beforeStr != "" && beforeStr != "0" {
		beforeMs, _ := strconv.ParseInt(beforeStr, 10, 64)
		beforeTime := time.UnixMilli(beforeMs).UTC()
		dbRows, queryErr = config.DB.Query(config.Ctx, `
			SELECT id, room_id, user_id, user_name, text, reply_to_id, mentions_ai, is_ai, timestamp
			FROM room_messages
			WHERE room_id = $1 AND timestamp < $2
			ORDER BY timestamp DESC LIMIT $3`,
			roomID, beforeTime, limit)
	} else {
		dbRows, queryErr = config.DB.Query(config.Ctx, `
			SELECT id, room_id, user_id, user_name, text, reply_to_id, mentions_ai, is_ai, timestamp
			FROM room_messages
			WHERE room_id = $1
			ORDER BY timestamp DESC LIMIT $2`,
			roomID, limit)
	}

	if queryErr != nil {
		log.Printf("❌ [GetRoomHistory] room=%s err=%v", roomID, queryErr)
		c.JSON(http.StatusInternalServerError, gin.H{"success": false})
		return
	}
	defer dbRows.Close()

	// Dùng pgx rows directly
	pgxRows := dbRows.(interface {
		Next() bool
		Close()
		Scan(...interface{}) error
	})

	var msgs []models.RoomChatMessage
	for pgxRows.Next() {
		var m models.RoomChatMessage
		var uid *string
		var replyTo *string
		if err := pgxRows.Scan(&m.ID, &m.RoomID, &uid, &m.UserName, &m.Text, &replyTo, &m.MentionsAI, &m.IsAI, &m.Timestamp); err != nil {
			log.Printf("⚠️ [GetRoomHistory] scan err: %v", err)
			continue
		}
		if replyTo != nil {
			m.ReplyToID = *replyTo
		}
		msgs = append(msgs, m)
	}

	// Đảo ngược để trả về thứ tự tăng dần (cũ → mới)
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}

	hasMore := len(msgs) == limit

	if msgs == nil {
		msgs = []models.RoomChatMessage{}
	}
	c.JSON(http.StatusOK, gin.H{
		"success":  true,
		"messages": msgs,
		"has_more": hasMore,
	})
}

// buildGroupRoomContext lấy toàn bộ chunks từ tài liệu trong phòng
func buildGroupRoomContext(roomID, query string) string {
	rows, err := config.DB.Query(config.Ctx, `
		SELECT DISTINCT doc_id
		FROM (
			SELECT id AS doc_id
			FROM documents
			WHERE room_id = $1 AND status = 'ready'

			UNION

			SELECT l.document_id AS doc_id
			FROM group_room_doc_links l
			JOIN documents d ON d.id = l.document_id
			WHERE l.room_id = $1 AND d.status = 'ready'
		) room_docs`, roomID)
	if err != nil {
		log.Printf("❌ [RoomAI] buildGroupRoomContext room=%s doc query error: %v", roomID, err)
		return ""
	}
	defer rows.Close()

	var docIDs []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		docIDs = append(docIDs, id)
	}
	if len(docIDs) == 0 {
		log.Printf("⚠️ [RoomAI] room=%s has no ready docs for query=%q", roomID, query)
		return "Chưa có tài liệu nào trong phòng."
	}
	log.Printf("📚 [RoomAI] room=%s query=%q doc_count=%d", roomID, query, len(docIDs))

	// Dùng hybrid_search hiện có với filter room docs
	chunks, err := utils.HybridSearchByDocIDs(docIDs, query, 8)
	if err != nil || len(chunks) == 0 {
		if err != nil {
			log.Printf("❌ [RoomAI] room=%s hybrid search error: %v", roomID, err)
		} else {
			log.Printf("⚠️ [RoomAI] room=%s hybrid search returned 0 chunks for query=%q", roomID, query)
		}
		return "Không tìm thấy nội dung liên quan trong tài liệu của phòng."
	}
	log.Printf("🔎 [RoomAI] room=%s hybrid search returned %d chunks", roomID, len(chunks))

	var sb strings.Builder
	for i, c := range chunks {
		content := strings.TrimSpace(c)
		preview := content
		if len(preview) > 180 {
			preview = preview[:180]
		}
		log.Printf("📄 [RoomAI] room=%s chunk[%d] content_len=%d preview=%q", roomID, i, len(content), preview)
		sb.WriteString(fmt.Sprintf("[%d]\n%s\n\n", i+1, content))
	}
	return sb.String()
}
