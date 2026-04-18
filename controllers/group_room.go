package controllers

import (
	"fmt"
	"log"
	"math/rand"
	"mindex-backend/config"
	"mindex-backend/internal/ws"
	"mindex-backend/models"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// ============================================================
// SPRINT 1: Room Lifecycle & Invite System
// ============================================================

// CreateRoom — POST /api/v1/rooms
func CreateRoom(c *gin.Context) {
	userID := c.GetString("user_id")
	var req struct {
		Name string `json:"name" binding:"required,max=100"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": "VALIDATION_ERROR", "message": "Tên phòng không hợp lệ"})
		return
	}

	// Kiểm tra user không có quá 3 phòng active
	var activeCount int
	config.DB.QueryRow(config.Ctx, `
		SELECT COUNT(*) FROM group_rooms 
		WHERE host_id = $1 AND status = 'active'`, userID).Scan(&activeCount)
	if activeCount >= 3 {
		c.JSON(403, gin.H{"success": false, "error": "ROOM_LIMIT", "message": "Bạn đang có 3 phòng active. Đóng phòng cũ để tạo phòng mới."})
		return
	}

	inviteCode := generateInviteCode()
	var roomID string
	err := config.DB.QueryRow(config.Ctx, `
		INSERT INTO group_rooms (invite_code, host_id, name)
		VALUES ($1, $2, $3)
		RETURNING id`, inviteCode, userID, req.Name).Scan(&roomID)
	if err != nil {
		log.Printf("❌ [CreateRoom] Insert error: %v", err)
		c.JSON(500, gin.H{"success": false, "message": "Không thể tạo phòng"})
		return
	}

	// Thêm host vào danh sách thành viên
	_, err = config.DB.Exec(config.Ctx, `
		INSERT INTO group_room_members (room_id, user_id, is_host)
		VALUES ($1, $2, true)`, roomID, userID)
	if err != nil {
		log.Printf("❌ [CreateRoom] Add member error: %v", err)
		c.JSON(500, gin.H{"success": false, "message": "Lỗi khi thêm host vào phòng"})
		return
	}

	// Cache invite code vào Redis (TTL 7 ngày)
	if config.RedisClient != nil {
		config.RedisClient.Set(config.Ctx, "room_invite:"+inviteCode, roomID, 7*24*time.Hour)
	}

	room := &models.GroupRoom{
		ID:         roomID,
		InviteCode: inviteCode,
		InviteLink: fmt.Sprintf("/rooms/join?code=%s", inviteCode),
		Name:       req.Name,
		MaxMembers: 5,
		Status:     "active",
	}

	c.JSON(201, gin.H{"success": true, "data": room})
}

// GetRoomInfo — GET /api/v1/rooms/info?code=...
func GetRoomInfo(c *gin.Context) {
	code := ValidateRoomInviteCode(c.Query("code"))
	if code == "" {
		c.JSON(400, gin.H{"success": false, "message": "Mã mời không hợp lệ"})
		return
	}

	var roomID, roomName, hostName string
	var memberCount, maxMembers int
	err := config.DB.QueryRow(config.Ctx, `
		SELECT r.id, r.name, u.name, r.max_members,
		       (SELECT COUNT(*) FROM group_room_members WHERE room_id = r.id AND left_at IS NULL)
		FROM group_rooms r
		JOIN users u ON r.host_id = u.id
		WHERE r.invite_code = $1 AND r.status = 'active'`, code).Scan(
		&roomID, &roomName, &hostName, &maxMembers, &memberCount)

	if err != nil {
		c.JSON(404, gin.H{"success": false, "message": "Phòng không tồn tại hoặc đã đóng"})
		return
	}

	c.JSON(200, gin.H{"success": true, "data": gin.H{
		"id":           roomID,
		"name":         roomName,
		"host_name":    hostName,
		"member_count": memberCount,
		"max_members":  maxMembers,
	}})
}

// JoinRoom — POST /api/v1/rooms/join
func JoinRoom(c *gin.Context) {
	userID := c.GetString("user_id")
	var req struct {
		InviteCode string `json:"invite_code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "message": "Thiếu mã mời"})
		return
	}

	inviteCode := ValidateRoomInviteCode(req.InviteCode)

	// 1. Tìm roomID từ DB (luôn check DB để chắc chắn status)
	var room models.GroupRoom
	var hostID *string
	err := config.DB.QueryRow(config.Ctx, `
		SELECT id, name, status, max_members, host_id
		FROM group_rooms WHERE invite_code = $1 AND status = 'active'`, inviteCode).Scan(
		&room.ID, &room.Name, &room.Status, &room.MaxMembers, &hostID)
	
	if err != nil {
		c.JSON(404, gin.H{"success": false, "error": "INVALID_CODE", "message": "Mã mời không hợp lệ hoặc phòng đã đóng"})
		return
	}

	roomID := room.ID

	// 2. Kiểm tra số lượng member hiện tại
	var memberCount int
	config.DB.QueryRow(config.Ctx, `
		SELECT COUNT(*) FROM group_room_members 
		WHERE room_id = $1 AND left_at IS NULL`, roomID).Scan(&memberCount)
	if memberCount >= room.MaxMembers {
		c.JSON(400, gin.H{"success": false, "error": "ROOM_FULL", "message": "Phòng đã đủ thành viên (tối đa 5 người)"})
		return
	}

	// 3. Kiểm tra đã là member chưa (có thể rejoin)
	var existingLeftAt *time.Time
	err = config.DB.QueryRow(config.Ctx, `
		SELECT left_at FROM group_room_members 
		WHERE room_id = $1 AND user_id = $2`, roomID, userID).Scan(&existingLeftAt)

	if err == nil && existingLeftAt == nil {
		c.JSON(400, gin.H{"success": false, "error": "ALREADY_MEMBER", "message": "Bạn đã là thành viên của phòng này"})
		return
	}

	if err == nil && existingLeftAt != nil {
		// Rejoin: reset left_at
		config.DB.Exec(config.Ctx, `
			UPDATE group_room_members SET left_at = NULL, joined_at = now()
			WHERE room_id = $1 AND user_id = $2`, roomID, userID)
	} else {
		// Join mới
		config.DB.Exec(config.Ctx, `
			INSERT INTO group_room_members (room_id, user_id)
			VALUES ($1, $2)`, roomID, userID)
	}

	// 4. Broadcast user_joined qua WS
	var userName string
	config.DB.QueryRow(config.Ctx, `SELECT name FROM users WHERE id = $1`, userID).Scan(&userName)
	ws.RoomHubInstance.BroadcastToRoom(roomID, models.RoomEvent{
		Type:   "user_joined",
		RoomID: roomID,
		UserID: userID,
		Payload: gin.H{"user_id": userID, "name": userName},
	})

	c.JSON(200, gin.H{"success": true, "data": gin.H{"id": roomID, "name": room.Name}})
}

// LeaveRoom — POST /api/v1/rooms/:id/leave
func LeaveRoom(c *gin.Context) {
	userID := c.GetString("user_id")
	roomID := c.Param("id")

	// Kiểm tra member hợp lệ
	var isHost bool
	err := config.DB.QueryRow(config.Ctx, `
		SELECT is_host FROM group_room_members 
		WHERE room_id = $1 AND user_id = $2 AND left_at IS NULL`, roomID, userID).Scan(&isHost)
	if err != nil {
		c.JSON(400, gin.H{"success": false, "message": "Bạn không ở trong phòng này"})
		return
	}

	// Set left_at
	config.DB.Exec(config.Ctx, `
		UPDATE group_room_members SET left_at = now(), is_host = false
		WHERE room_id = $1 AND user_id = $2`, roomID, userID)

	// Nếu host rời → chuyển host
	if isHost {
		transferRoomHost(roomID, userID)
	}

	// Broadcast user_left
	var userName string
	config.DB.QueryRow(config.Ctx, `SELECT name FROM users WHERE id = $1`, userID).Scan(&userName)
	ws.RoomHubInstance.BroadcastToRoom(roomID, models.RoomEvent{
		Type:   "user_left",
		RoomID: roomID,
		UserID: userID,
		Payload: gin.H{"user_id": userID, "name": userName},
	})

	c.JSON(200, gin.H{"success": true, "message": "Đã rời phòng"})
}

// GetRoom — GET /api/v1/rooms/:id
func GetRoom(c *gin.Context) {
	userID := c.GetString("user_id")
	roomID := c.Param("id")

	// Kiểm tra user có quyền xem phòng
	var isMember bool
	config.DB.QueryRow(config.Ctx, `
		SELECT EXISTS(SELECT 1 FROM group_room_members 
		WHERE room_id = $1 AND user_id = $2 AND left_at IS NULL)`, roomID, userID).Scan(&isMember)
	if !isMember {
		c.JSON(403, gin.H{"success": false, "message": "Bạn không phải thành viên của phòng này"})
		return
	}

	room := getRoomDetail(roomID)
	if room == nil {
		c.JSON(404, gin.H{"success": false, "message": "Không tìm thấy phòng"})
		return
	}

	// Đánh dấu ai đang online từ WS Hub
	onlineSet := make(map[string]bool)
	for _, uid := range ws.RoomHubInstance.GetOnlineUsers(roomID) {
		onlineSet[uid] = true
	}
	for i := range room.Members {
		room.Members[i].IsOnline = onlineSet[room.Members[i].UserID]
	}

	c.JSON(200, gin.H{"success": true, "data": room})
}

// CloseRoom — POST /api/v1/rooms/:id/close (host only)
func CloseRoom(c *gin.Context) {
	userID := c.GetString("user_id")
	roomID := c.Param("id")

	var isHost bool
	config.DB.QueryRow(config.Ctx, `
		SELECT is_host FROM group_room_members 
		WHERE room_id = $1 AND user_id = $2 AND left_at IS NULL`, roomID, userID).Scan(&isHost)
	if !isHost {
		c.JSON(403, gin.H{"success": false, "message": "Chỉ host mới có thể đóng phòng"})
		return
	}

	config.DB.Exec(config.Ctx, `
		UPDATE group_rooms SET status = 'closed', closed_at = now()
		WHERE id = $1`, roomID)

	// Xóa invite code khỏi Redis
	var inviteCode string
	config.DB.QueryRow(config.Ctx, `SELECT invite_code FROM group_rooms WHERE id = $1`, roomID).Scan(&inviteCode)
	if config.RedisClient != nil && inviteCode != "" {
		config.RedisClient.Del(config.Ctx, "room_invite:"+inviteCode)
	}

	ws.RoomHubInstance.BroadcastToRoom(roomID, models.RoomEvent{
		Type:    "room_closed",
		RoomID:  roomID,
		Payload: gin.H{"message": "Host đã đóng phòng"},
	})

	c.JSON(200, gin.H{"success": true, "message": "Phòng đã được đóng"})
}

// GetMyRooms — GET /api/v1/rooms/my
func GetMyRooms(c *gin.Context) {
	userID := c.GetString("user_id")

	rows, err := config.DB.Query(config.Ctx, `
		SELECT gr.id, gr.name, gr.invite_code, gr.status, gr.max_members, 
		       gr.host_id, gr.created_at, grm.is_host,
		       (SELECT COUNT(*) FROM group_room_members WHERE room_id = gr.id AND left_at IS NULL) as member_count
		FROM group_rooms gr
		JOIN group_room_members grm ON gr.id = grm.room_id
		WHERE grm.user_id = $1 AND grm.left_at IS NULL AND gr.status = 'active'
		ORDER BY gr.created_at DESC`, userID)
	if err != nil {
		log.Printf("❌ [GetMyRooms] Query error: %v", err)
		c.JSON(500, gin.H{"success": false, "message": "Lỗi truy vấn"})
		return
	}
	defer rows.Close()

	var rooms []gin.H
	for rows.Next() {
		var (
			id, name, inviteCode, status string
			maxMembers                   int
			memberCount                  int64
			hostID                       *string
			createdAt                    time.Time
			isHost                       bool
		)
		err := rows.Scan(&id, &name, &inviteCode, &status, &maxMembers, &hostID, &createdAt, &isHost, &memberCount)
		if err != nil {
			fmt.Printf("❌ [GetMyRooms] Scan error: %v\n", err)
			continue
		}
		rooms = append(rooms, gin.H{
			"id":          id,
			"name":        name,
			"invite_code": inviteCode,
			"invite_link": fmt.Sprintf("/rooms/join?code=%s", inviteCode),
			"status":      status,
			"max_members": maxMembers,
			"member_count": memberCount,
			"is_host":     isHost,
			"created_at":  createdAt,
		})
	}

	if rooms == nil {
		rooms = []gin.H{}
	}
	c.JSON(200, gin.H{"success": true, "data": rooms})
}

// GetRoomDocs — GET /api/v1/rooms/:id/docs
func GetRoomDocs(c *gin.Context) {
	userID := c.GetString("user_id")
	roomID := c.Param("id")

	var isMember bool
	config.DB.QueryRow(config.Ctx, `
		SELECT EXISTS(SELECT 1 FROM group_room_members 
		WHERE room_id = $1 AND user_id = $2 AND left_at IS NULL)`, roomID, userID).Scan(&isMember)
	if !isMember {
		c.JSON(403, gin.H{"success": false, "message": "Không có quyền"})
		return
	}

	rows, err := config.DB.Query(config.Ctx, `
		SELECT d.id, d.title, d.status, d.user_id, u.name, d.created_at
		FROM documents d
		JOIN users u ON d.user_id = u.id
		WHERE d.room_id = $1
		ORDER BY d.created_at ASC`, roomID)
	if err != nil {
		log.Printf("❌ [GetRoomDocs] Query error: %v", err)
		c.JSON(500, gin.H{"success": false, "message": "Lỗi truy vấn"})
		return
	}
	defer rows.Close()

	var docs []models.RoomDocument
	for rows.Next() {
		var doc models.RoomDocument
		rows.Scan(&doc.ID, &doc.Title, &doc.Status, &doc.OwnerID, &doc.OwnerName, &doc.UploadedAt)
		doc.IsOwn = doc.OwnerID == userID
		docs = append(docs, doc)
	}
	if docs == nil {
		docs = []models.RoomDocument{}
	}
	c.JSON(200, gin.H{"success": true, "data": docs})
}

// ============================================================
// Helpers
// ============================================================

func generateInviteCode() string {
	const chars = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	b := make([]byte, 8)
	for i := range b {
		b[i] = chars[r.Intn(len(chars))]
	}
	return string(b)
}

func transferRoomHost(roomID, leavingHostID string) {
	var newHostID string
	err := config.DB.QueryRow(config.Ctx, `
		SELECT user_id FROM group_room_members
		WHERE room_id = $1 AND user_id != $2 AND left_at IS NULL
		ORDER BY joined_at ASC LIMIT 1`, roomID, leavingHostID).Scan(&newHostID)

	if err != nil || newHostID == "" {
		// Không còn ai → đóng phòng
		config.DB.Exec(config.Ctx, `
			UPDATE group_rooms SET status = 'closed', closed_at = now()
			WHERE id = $1`, roomID)
		ws.RoomHubInstance.BroadcastToRoom(roomID, models.RoomEvent{
			Type: "room_closed", RoomID: roomID,
			Payload: gin.H{"message": "Không còn thành viên, phòng đã đóng"},
		})
		return
	}

	config.DB.Exec(config.Ctx, `
		UPDATE group_room_members SET is_host = true
		WHERE room_id = $1 AND user_id = $2`, roomID, newHostID)
	config.DB.Exec(config.Ctx, `
		UPDATE group_rooms SET host_id = $1 WHERE id = $2`, newHostID, roomID)

	var newHostName string
	config.DB.QueryRow(config.Ctx, `SELECT name FROM users WHERE id = $1`, newHostID).Scan(&newHostName)
	ws.RoomHubInstance.BroadcastToRoom(roomID, models.RoomEvent{
		Type: "host_changed", RoomID: roomID,
		Payload: gin.H{"new_host_id": newHostID, "new_host_name": newHostName},
	})
}

func getRoomDetail(roomID string) *models.GroupRoom {
	var room models.GroupRoom
	var hostID *string
	err := config.DB.QueryRow(config.Ctx, `
		SELECT id, name, invite_code, status, max_members, host_id, created_at
		FROM group_rooms WHERE id = $1`, roomID).Scan(
		&room.ID, &room.Name, &room.InviteCode, &room.Status,
		&room.MaxMembers, &hostID, &room.CreatedAt)
	if err != nil {
		return nil
	}
	room.HostID = hostID
	room.InviteLink = fmt.Sprintf("https://mindex.io.vn/rooms/join?code=%s", room.InviteCode)

	rows, _ := config.DB.Query(config.Ctx, `
		SELECT grm.user_id, u.name, grm.joined_at, grm.doc_count, grm.is_host
		FROM group_room_members grm
		JOIN users u ON grm.user_id = u.id
		WHERE grm.room_id = $1 AND grm.left_at IS NULL
		ORDER BY grm.joined_at ASC`, roomID)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var m models.GroupRoomMember
			rows.Scan(&m.UserID, &m.Name, &m.JoinedAt, &m.DocCount, &m.IsHost)
			m.RoomID = roomID
			room.Members = append(room.Members, m)
		}
	}
	room.MemberCount = len(room.Members)

	// Đếm tổng tài liệu
	config.DB.QueryRow(config.Ctx, `
		SELECT COUNT(*) FROM documents WHERE room_id = $1 AND status = 'ready'`, roomID).Scan(&room.TotalDocs)

	return &room
}

// IsRoomMember kiểm tra user có phải active member của phòng không
func IsRoomMember(roomID, userID string) bool {
	var exists bool
	config.DB.QueryRow(config.Ctx, `
		SELECT EXISTS(SELECT 1 FROM group_room_members 
		WHERE room_id = $1 AND user_id = $2 AND left_at IS NULL)`, roomID, userID).Scan(&exists)
	return exists
}

// ValidateRoomInviteCode parse invite_code từ full link hoặc code
func ValidateRoomInviteCode(input string) string {
	// Nếu là link dạng https://mindex.io.vn/rooms/join?code=ABCD1234
	re := regexp.MustCompile(`code=([A-Z0-9]{8})`)
	if matches := re.FindStringSubmatch(input); len(matches) > 1 {
		return matches[1]
	}
	// Nếu là code thuần
	return strings.ToUpper(strings.TrimSpace(input))
}
