package controllers

import (
	"context"
	"encoding/json"
	"log"
	"mindex-backend/config"
	"mindex-backend/internal/ws"
	"mindex-backend/utils"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Cần điều chỉnh theo CORS thực tế của bạn
	},
}

// CreateFeedbackSession tạo một phiên đóng góp ý kiến mới
func CreateFeedbackSession(c *gin.Context) {
	userID := c.GetString("user_id")
	var req struct {
		Subject string `json:"subject" binding:"required"`
		Message string `json:"message" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "Thiếu tiêu đề hoặc nội dung"})
		return
	}

	tx, err := config.DB.Begin(config.Ctx)
	if err != nil {
		c.JSON(500, gin.H{"error": "Database error"})
		return
	}
	defer tx.Rollback(config.Ctx)

	var sessionID string
	err = tx.QueryRow(config.Ctx, 
		`INSERT INTO feedback_sessions (user_id, subject) VALUES ($1, $2) RETURNING id`,
		userID, req.Subject).Scan(&sessionID)
	if err != nil {
		c.JSON(500, gin.H{"error": "Không thể tạo phiên"})
		return
	}

	_, err = tx.Exec(config.Ctx,
		`INSERT INTO feedback_messages (session_id, sender_id, role, content) VALUES ($1, $2, 'user', $3)`,
		sessionID, userID, req.Message)
	if err != nil {
		c.JSON(500, gin.H{"error": "Không thể lưu tin nhắn"})
		return
	}

	tx.Commit(config.Ctx)

	// Thông báo cho admin
	ws.GlobalHub.SendToAdmins(gin.H{
		"type": "new_session",
		"payload": gin.H{
			"id":         sessionID,
			"subject":    req.Subject,
			"user_id":    userID,
			"created_at": time.Now(),
		},
	})

	c.JSON(201, gin.H{"success": true, "session_id": sessionID})
}

// GetFeedbackSessions lấy danh sách các phiên
func GetFeedbackSessions(c *gin.Context) {
	userID := c.GetString("user_id")
	role := c.GetString("role")

	var query string
	var args []interface{}

	if role == "admin" {
		query = `SELECT s.id, s.user_id, s.admin_id, s.subject, s.status, s.created_at, u.name as user_name, COALESCE(u.avatar_url, '') as avatar_url,
		               COALESCE((SELECT content FROM feedback_messages WHERE session_id = s.id ORDER BY created_at DESC LIMIT 1), '') as last_message,
					   (SELECT created_at FROM feedback_messages WHERE session_id = s.id ORDER BY created_at DESC LIMIT 1) as last_message_at
		         FROM feedback_sessions s
				 JOIN users u ON s.user_id = u.id
				 ORDER BY last_message_at DESC NULLS LAST`
	} else {
		query = `SELECT s.id, s.user_id, s.admin_id, s.subject, s.status, s.created_at, u.name as user_name, COALESCE(u.avatar_url, '') as avatar_url,
		               COALESCE((SELECT content FROM feedback_messages WHERE session_id = s.id ORDER BY created_at DESC LIMIT 1), '') as last_message,
					   (SELECT created_at FROM feedback_messages WHERE session_id = s.id ORDER BY created_at DESC LIMIT 1) as last_message_at
		         FROM feedback_sessions s
				 JOIN users u ON s.user_id = u.id
				 WHERE s.user_id = $1
				 ORDER BY last_message_at DESC NULLS LAST`
		args = append(args, userID)
	}

	rows, err := config.DB.Query(config.Ctx, query, args...)
	if err != nil {
		c.JSON(500, gin.H{"error": "Query error: " + err.Error()})
		return
	}
	defer rows.Close()

	var sessions []interface{}
	for rows.Next() {
		var s struct {
			ID            string     `json:"id"`
			UserID        string     `json:"user_id"`
			AdminID       *string    `json:"admin_id"`
			Subject       string     `json:"subject"`
			Status        string     `json:"status"`
			CreatedAt     time.Time  `json:"created_at"`
			UserName      string     `json:"user_name"`
			AvatarURL     string     `json:"avatar_url"`
			LastMessage   string     `json:"last_message"`
			LastMessageAt *time.Time `json:"last_message_at"`
		}
		err := rows.Scan(&s.ID, &s.UserID, &s.AdminID, &s.Subject, &s.Status, &s.CreatedAt, &s.UserName, &s.AvatarURL, &s.LastMessage, &s.LastMessageAt)
		if err != nil {
			log.Printf("⚠️ Error scanning feedback session row: %v", err)
			continue
		}
		sessions = append(sessions, s)
	}

	c.JSON(200, gin.H{"success": true, "data": sessions})
}

// GetFeedbackMessages lấy lịch sử tin nhắn
func GetFeedbackMessages(c *gin.Context) {
	sessionID := c.Param("id")
	userID := c.GetString("user_id")
	role := c.GetString("role")

	// Kiểm tra quyền
	if role != "admin" {
		var exists bool
		config.DB.QueryRow(config.Ctx, `SELECT EXISTS(SELECT 1 FROM feedback_sessions WHERE id = $1 AND user_id = $2)`, sessionID, userID).Scan(&exists)
		if !exists {
			c.JSON(403, gin.H{"error": "Permission denied"})
			return
		}
	}

	rows, err := config.DB.Query(config.Ctx, 
		`SELECT m.id, m.sender_id, m.role, m.content, m.created_at, u.name as sender_name
		 FROM feedback_messages m
		 JOIN users u ON m.sender_id = u.id
		 WHERE m.session_id = $1 ORDER BY m.created_at ASC`, sessionID)
	if err != nil {
		c.JSON(500, gin.H{"error": "Query error"})
		return
	}
	defer rows.Close()

	var messages []interface{}
	for rows.Next() {
		var m struct {
			ID         string    `json:"id"`
			SenderID   string    `json:"sender_id"`
			Role       string    `json:"role"`
			Content    string    `json:"content"`
			CreatedAt  time.Time `json:"created_at"`
			SenderName string    `json:"sender_name"`
		}
		rows.Scan(&m.ID, &m.SenderID, &m.Role, &m.Content, &m.CreatedAt, &m.SenderName)
		messages = append(messages, m)
	}

	c.JSON(200, gin.H{"success": true, "data": messages})
}

// ServeFeedbackWS xử lý kết nối WebSocket
func ServeFeedbackWS(c *gin.Context) {
	token := c.Query("token")
	if token == "" {
		// Fallback: Thử lấy từ cookie
		cookieToken, err := c.Cookie("access_token")
		if err == nil {
			token = cookieToken
		}
	}

	if token == "" {
		c.JSON(401, gin.H{"error": "Token missing"})
		return
	}

	claims, err := utils.VerifyToken(token, false)
	if err != nil {
		c.JSON(401, gin.H{"error": "Invalid token"})
		return
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("WS Upgrade Error: %v", err)
		return
	}

	client := &ws.Client{
		Hub:    ws.GlobalHub,
		Conn:   conn,
		UserID: claims.UserID,
		IsAdmin: claims.Role == "admin",
		Send:   make(chan []byte, 256),
	}

	client.Hub.Register <- client

	// Khởi chạy các goroutine xử lý read/write cho client này
	go clientWritePump(client)
	go clientReadPump(client)
}

func clientReadPump(c *ws.Client) {
	defer func() {
		c.Hub.Unregister <- c
		c.Conn.Close()
	}()

	for {
		_, message, err := c.Conn.ReadMessage()
		if err != nil {
			break
		}

		var wsMsg struct {
			Type      string `json:"type"`
			SessionID string `json:"session_id"`
			Content   string `json:"content"`
		}

		if err := json.Unmarshal(message, &wsMsg); err != nil {
			continue
		}

		if wsMsg.Type == "chat" {
			handleIncomingChat(c, wsMsg.SessionID, wsMsg.Content)
		}
	}
}

func clientWritePump(c *ws.Client) {
	defer func() {
		c.Conn.Close()
	}()
	for {
		select {
		case message, ok := <-c.Send:
			if !ok {
				c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			c.Conn.WriteMessage(websocket.TextMessage, message)
		}
	}
}

func handleIncomingChat(c *ws.Client, sessionID string, content string) {
	// 1. Lưu vào DB
	role := "user"
	if c.IsAdmin {
		role = "admin"
	}

	_, err := config.DB.Exec(context.Background(),
		`INSERT INTO feedback_messages (session_id, sender_id, role, content) VALUES ($1, $2, $3, $4)`,
		sessionID, c.UserID, role, content)
	if err != nil {
		log.Printf("DB Save Message Error: %v", err)
		return
	}

	// 2. Tìm người nhận. Nếu là user gửi -> admin nhận. Nếu admin gửi -> user nhận (+ các admin khác).
	msgPayload := gin.H{
		"type":       "chat",
		"session_id": sessionID,
		"payload": gin.H{
			"sender_id":  c.UserID,
			"role":       role,
			"content":    content,
			"created_at": time.Now(),
		},
	}

	if !c.IsAdmin {
		// User gửi -> Thông báo cho Admin
		ws.GlobalHub.SendToAdmins(msgPayload)
		// Tìm adminID của session (nếu có) để bắn private nếu muốn, hoặc cứ broadcast admin
	} else {
		// Admin gửi -> Thông báo cho User gốc của session
		var sessionUserID string
		config.DB.QueryRow(context.Background(), `SELECT user_id FROM feedback_sessions WHERE id = $1`, sessionID).Scan(&sessionUserID)
		ws.GlobalHub.SendToUser(sessionUserID, msgPayload)
		
		// Cũng thông báo cho các admin khác đang xem
		ws.GlobalHub.SendToAdmins(msgPayload)
	}
}
