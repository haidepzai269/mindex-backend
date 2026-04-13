package ws

import (
	"encoding/json"
	"log"
	"sync"

	"github.com/gorilla/websocket"
)

// Message tượng trưng cho cấu trúc dữ liệu gửi qua WS
type Message struct {
	Type      string      `json:"type"`      // ping, feedback_message, session_update
	SessionID string      `json:"session_id,omitempty"`
	Payload   interface{} `json:"payload"`
}

// Client đại diện cho một kết nối WebSocket
type Client struct {
	Hub      *Hub
	Conn     *websocket.Conn
	UserID   string
	IsAdmin  bool
	Send     chan []byte
}

// Hub quản lý tất cả các kết nối đang hoạt động
type Hub struct {
	// Registered clients. Map userID -> []*Client (một user có thể mở nhiều tab)
	Clients map[string][]*Client
	
	// Register requests from the clients.
	Register chan *Client
	
	// Unregister requests from clients.
	Unregister chan *Client
	
	// Inbound messages from the clients.
	Broadcast chan []byte

	mu sync.RWMutex
}

func NewHub() *Hub {
	return &Hub{
		Clients:    make(map[string][]*Client),
		Register:   make(chan *Client),
		Unregister: make(chan *Client),
		Broadcast:  make(chan []byte),
	}
}

func (h *Hub) Run() {
	for {
		select {
		case client := <-h.Register:
			h.mu.Lock()
			h.Clients[client.UserID] = append(h.Clients[client.UserID], client)
			h.mu.Unlock()
			log.Printf("🔌 Client registered: %s (Admin: %v)", client.UserID, client.IsAdmin)

		case client := <-h.Unregister:
			h.mu.Lock()
			clients := h.Clients[client.UserID]
			for i, c := range clients {
				if c == client {
					h.Clients[client.UserID] = append(clients[:i], clients[i+1:]...)
					break
				}
			}
			if len(h.Clients[client.UserID]) == 0 {
				delete(h.Clients, client.UserID)
			}
			close(client.Send)
			h.mu.Unlock()
			log.Printf("❌ Client unregistered: %s", client.UserID)

		case message := <-h.Broadcast:
			// Tạm thời chưa dùng broadcast toàn bộ, sẽ dùng SendToUser/SendToAdmins
			_ = message
		}
	}
}

// SendToUser gửi tin nhắn tới toàn bộ các tab đang mở của một user
func (h *Hub) SendToUser(userID string, msg interface{}) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	clients, ok := h.Clients[userID]
	if !ok {
		return
	}

	payload, _ := json.Marshal(msg)
	for _, client := range clients {
		select {
		case client.Send <- payload:
		default:
			// Nếu buffer đầy, unregister client này
			go func(c *Client) { h.Unregister <- c }(client)
		}
	}
}

// SendToAdmins gửi tin nhắn tới tất cả admin đang online
func (h *Hub) SendToAdmins(msg interface{}) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	payload, _ := json.Marshal(msg)
	for _, clients := range h.Clients {
		for _, client := range clients {
			if client.IsAdmin {
				select {
				case client.Send <- payload:
				default:
					go func(c *Client) { h.Unregister <- c }(client)
				}
			}
		}
	}
}

// GlobalHub là instance duy nhất dùng cho ứng dụng
var GlobalHub = NewHub()
