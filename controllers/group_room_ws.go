package controllers

import (
	"encoding/json"
	"fmt"
	"mindex-backend/config"
	"mindex-backend/internal/ws"
	"mindex-backend/models"
	"mindex-backend/utils"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	gorillaws "github.com/gorilla/websocket"
)

var roomWsUpgrader = gorillaws.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
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

	conn, _ := roomWsUpgrader.Upgrade(c.Writer, c.Request, nil)

	client := &ws.RoomClient{
		Hub:    ws.RoomHubInstance,
		Conn:   conn,
		UserID: userID,
		RoomID: roomID,
		Send:   make(chan []byte, 128),
	}

	ws.RoomHubInstance.Register <- client

	// Gửi 20 tin nhắn gần nhất cho client vừa join
	go sendRoomHistory(client, roomID)

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

// handleRoomIncomingMessage xử lý tin nhắn đến từ client WS
func handleRoomIncomingMessage(client *ws.RoomClient, roomID, userID string, raw []byte) {
	var incoming struct {
		Type string `json:"type"`
		Text string `json:"text"`
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
				"1", 30*time.Second)
		}

	case "chat_message":
		if strings.TrimSpace(incoming.Text) == "" {
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
			Timestamp:  time.Now(),
		}

		// Broadcast tin nhắn tới tất cả trong phòng
		ws.RoomHubInstance.BroadcastToRoom(roomID, models.RoomEvent{
			Type: "chat_message", RoomID: roomID, UserID: userID,
			Payload: msg,
		})

		// Lưu vào Redis history (LPUSH + LTRIM giữ 50 tin)
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
	}
}

// sendRoomHistory gửi 20 tin nhắn gần nhất khi user vào phòng
func sendRoomHistory(client *ws.RoomClient, roomID string) {
	if config.RedisClient == nil {
		return
	}
	key := fmt.Sprintf("room_chat_history:%s", roomID)
	msgs, err := config.RedisClient.LRange(config.Ctx, key, 0, 19).Result()
	if err != nil || len(msgs) == 0 {
		return
	}

	// Đảo ngược để gửi theo thứ tự cũ → mới
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}

	var history []json.RawMessage
	for _, m := range msgs {
		history = append(history, json.RawMessage(m))
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
	chatHistory := getRoomChatHistory(roomID, 10)

	systemMsg := fmt.Sprintf(`Bạn là MindexAI, trợ lý học tập cho phòng học nhóm. Hãy trả lời dựa trên tài liệu bên dưới.

=== TÀI LIỆU TRONG PHÒNG ===
%s

=== LỊCH SỞ CHAT ===
%s`, ragContext, chatHistory)

	messages := []utils.ChatMessage{
		{Role: "system", Content: systemMsg},
		{Role: "user", Content: query},
	}

	answer, _, err := utils.AI.ChatNonStream(utils.ServiceChat, messages)
	if err != nil {
		ws.RoomHubInstance.BroadcastToRoom(roomID, models.RoomEvent{
			Type: "ai_error", RoomID: roomID,
			Payload: gin.H{"message": "MindexAI không phản hồi, thử lại sau."},
		})
		return
	}

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

// buildGroupRoomContext lấy toàn bộ chunks từ tài liệu trong phòng
func buildGroupRoomContext(roomID, query string) string {
	rows, err := config.DB.Query(config.Ctx, `
		SELECT id FROM documents 
		WHERE room_id = $1 AND status = 'ready'`, roomID)
	if err != nil {
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
		return "Chưa có tài liệu nào trong phòng."
	}

	// Dùng hybrid_search hiện có với filter room docs
	chunks, err := utils.HybridSearchByDocIDs(docIDs, query, 8)
	if err != nil || len(chunks) == 0 {
		return "Không tìm thấy nội dung liên quan trong tài liệu của phòng."
	}

	var sb strings.Builder
	for i, c := range chunks {
		sb.WriteString(fmt.Sprintf("[%d] %s\n\n", i+1, c))
	}
	return sb.String()
}
