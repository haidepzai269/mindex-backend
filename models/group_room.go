package models

import "time"

// GroupRoom đại diện cho một phòng học nhóm
type GroupRoom struct {
	ID          string     `json:"id"`
	InviteCode  string     `json:"invite_code"`
	InviteLink  string     `json:"invite_link,omitempty"` // computed
	HostID      *string    `json:"host_id,omitempty"`
	Name        string     `json:"name"`
	MaxMembers  int        `json:"max_members"`
	Status      string     `json:"status"` // active | closed | archived
	CreatedAt   time.Time  `json:"created_at"`
	ClosedAt    *time.Time `json:"closed_at,omitempty"`
	// Computed fields
	MemberCount int               `json:"member_count,omitempty"`
	Members     []GroupRoomMember `json:"members,omitempty"`
	TotalDocs   int               `json:"total_docs,omitempty"`
}

// GroupRoomMember đại diện thông tin 1 thành viên trong phòng
type GroupRoomMember struct {
	RoomID   string     `json:"room_id"`
	UserID   string     `json:"user_id"`
	Name     string     `json:"name"`
	Email    string     `json:"email,omitempty"`
	JoinedAt time.Time  `json:"joined_at"`
	LeftAt   *time.Time `json:"left_at,omitempty"`
	DocCount int        `json:"doc_count"`
	IsHost   bool       `json:"is_host"`
	IsOnline bool       `json:"is_online"` // computed từ Redis heartbeat
}

// RoomDocument tài liệu trong phòng (có thêm owner info)
type RoomDocument struct {
	ID         string     `json:"id"`
	Title      string     `json:"title"`
	Status     string     `json:"status"`
	OwnerID    string     `json:"owner_id"`
	OwnerName  string     `json:"owner_name"`
	IsOwn      bool       `json:"is_own"` // true nếu là tài liệu của mình
	UploadedAt time.Time  `json:"uploaded_at"`
}

// RoomChatMessage tin nhắn trong phòng
type RoomChatMessage struct {
	ID          string      `json:"id"`
	RoomID      string      `json:"room_id"`
	UserID      string      `json:"user_id,omitempty"` // empty nếu là AI
	UserName    string      `json:"user_name"`
	AvatarColor string      `json:"avatar_color,omitempty"`
	Text        string      `json:"text"`
	MentionsAI  bool        `json:"mentions_ai,omitempty"`
	Mentions    []string    `json:"mentions,omitempty"` // userIDs được @tag
	IsAI        bool               `json:"is_ai,omitempty"`
	Reactions   map[string][]string `json:"reactions,omitempty"` // emoji -> []userIDs
	ReplyToID   string             `json:"reply_to_id,omitempty"`
	Timestamp   time.Time          `json:"timestamp"`
}

// RoomEvent - event gửi qua WebSocket
type RoomEvent struct {
	Type    string      `json:"type"`
	// "user_joined" | "user_left" | "doc_uploaded" | "chat_message"
	// "ai_chunk" | "ai_done" | "ai_error" | "ai_busy" | "host_changed"
	RoomID  string      `json:"room_id"`
	UserID  string      `json:"user_id,omitempty"`
	Payload interface{} `json:"payload"`
}
