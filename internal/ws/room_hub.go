package ws

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// RoomClient đại diện kết nối WS của một thành viên trong phòng
type RoomClient struct {
	Hub    *RoomHub
	Conn   *websocket.Conn
	UserID string
	RoomID string
	Send   chan []byte
}

// RoomHub quản lý tất cả kết nối WS theo phòng
type RoomHub struct {
	// rooms map: roomID -> set of *RoomClient
	rooms map[string]map[*RoomClient]bool
	// timers map: roomID:userID -> timer for offline broadcast
	timers map[string]*time.Timer
	mu     sync.RWMutex

	Register   chan *RoomClient
	Unregister chan *RoomClient
}

func NewRoomHub() *RoomHub {
	return &RoomHub{
		rooms:      make(map[string]map[*RoomClient]bool),
		timers:     make(map[string]*time.Timer),
		Register:   make(chan *RoomClient, 64),
		Unregister: make(chan *RoomClient, 64),
	}
}

func (h *RoomHub) Run() {
	for {
		select {
		case client := <-h.Register:
			h.mu.Lock()
			if _, ok := h.rooms[client.RoomID]; !ok {
				h.rooms[client.RoomID] = make(map[*RoomClient]bool)
			}
			h.rooms[client.RoomID][client] = true

			// Hủy timer offline nếu đang đếm ngược
			timerKey := fmt.Sprintf("%s:%s", client.RoomID, client.UserID)
			if timer, ok := h.timers[timerKey]; ok {
				timer.Stop()
				delete(h.timers, timerKey)
				log.Printf("⏱️ [RoomHub] Cancelled offline timer for user %s", client.UserID)
			}
			h.mu.Unlock()
			log.Printf("🏠 [RoomHub] User %s joined room %s", client.UserID, client.RoomID)

		case client := <-h.Unregister:
			h.mu.Lock()
			if roomClients, ok := h.rooms[client.RoomID]; ok {
				if _, ok := roomClients[client]; ok {
					delete(roomClients, client)
					close(client.Send)

					// Kiểm tra xem user còn kết nối nào khác trong phòng không
					stillHasConn := false
					for c := range roomClients {
						if c.UserID == client.UserID {
							stillHasConn = true
							break
						}
					}

					// Nếu không còn kết nối nào -> đặt timer broadcast offline sau 2 phút
					if !stillHasConn {
						timerKey := fmt.Sprintf("%s:%s", client.RoomID, client.UserID)
						// Dùng biến local để tránh closure capture sai
						rid := client.RoomID
						uid := client.UserID

						h.timers[timerKey] = time.AfterFunc(2*time.Minute, func() {
							h.mu.Lock()
							delete(h.timers, timerKey)
							h.mu.Unlock()

							// Broadcast sự kiện offline tới mọi người trong phòng
							// (Ở đây ta dùng event type "user_offline" hoặc "user_left")
							h.BroadcastToRoom(rid, struct {
								Type    string `json:"type"`
								RoomID  string `json:"room_id"`
								UserID  string `json:"user_id"`
								Payload interface{} `json:"payload"`
							}{
								Type:   "user_offline",
								RoomID: rid,
								UserID: uid,
								Payload: map[string]string{
									"user_id": uid,
									"status":  "offline",
								},
							})
							log.Printf("📡 [RoomHub] Broadcasted user_offline for %s", uid)
						})
						log.Printf("⏱️ [RoomHub] Started offline timer for user %s (2m grace period)", client.UserID)
					}

					if len(roomClients) == 0 {
						delete(h.rooms, client.RoomID)
					}
				}
			}
			h.mu.Unlock()
			log.Printf("🚪 [RoomHub] User %s disconnected from room %s", client.UserID, client.RoomID)
		}
	}
}

// BroadcastToRoom gửi message tới tất cả client trong phòng
func (h *RoomHub) BroadcastToRoom(roomID string, msg interface{}) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	clients, ok := h.rooms[roomID]
	if !ok {
		return
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		return
	}

	for client := range clients {
		select {
		case client.Send <- payload:
		default:
			go func(c *RoomClient) { h.Unregister <- c }(client)
		}
	}
}

// BroadcastToRoomExcept gửi message tới tất cả client trừ 1 người
func (h *RoomHub) BroadcastToRoomExcept(roomID, excludeUserID string, msg interface{}) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	clients, ok := h.rooms[roomID]
	if !ok {
		return
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		return
	}

	for client := range clients {
		if client.UserID == excludeUserID {
			continue
		}
		select {
		case client.Send <- payload:
		default:
			go func(c *RoomClient) { h.Unregister <- c }(client)
		}
	}
}

// GetOnlineUsers lấy danh sách userID đang kết nối WS trong phòng
func (h *RoomHub) GetOnlineUsers(roomID string) []string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var userIDs []string
	seen := make(map[string]bool)

	clients, ok := h.rooms[roomID]
	if !ok {
		return userIDs
	}

	for client := range clients {
		if !seen[client.UserID] {
			userIDs = append(userIDs, client.UserID)
			seen[client.UserID] = true
		}
	}
	return userIDs
}

// GetRoomClientCount đếm số client đang kết nối trong phòng
func (h *RoomHub) GetRoomClientCount(roomID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.rooms[roomID])
}

// readPump đọc message từ client WS
func (c *RoomClient) ReadPump(onMessage func(msg []byte)) {
	defer func() {
		c.Hub.Unregister <- c
		c.Conn.Close()
	}()
	for {
		_, message, err := c.Conn.ReadMessage()
		if err != nil {
			break
		}
		if onMessage != nil {
			onMessage(message)
		}
	}
}

// writePump ghi message vào kết nối WS
func (c *RoomClient) WritePump() {
	defer c.Conn.Close()
	for {
		msg, ok := <-c.Send
		if !ok {
			c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
			return
		}
		if err := c.Conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			return
		}
	}
}

// RoomHubInstance là singleton RoomHub toàn cục
var RoomHubInstance = NewRoomHub()
