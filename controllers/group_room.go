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

	// Đánh dấu ai đang online từ Redis (với grace period)
	for i := range room.Members {
		if config.RedisClient != nil {
			exists, _ := config.RedisClient.Exists(config.Ctx, fmt.Sprintf("room_online:%s:%s", roomID, room.Members[i].UserID)).Result()
			room.Members[i].IsOnline = exists > 0
		} else {
			// Fallback về WS Hub nếu Redis lỗi
			onlineUsers := ws.RoomHubInstance.GetOnlineUsers(roomID)
			isOnline := false
			for _, uid := range onlineUsers {
				if uid == room.Members[i].UserID {
					isOnline = true
					break
				}
			}
			room.Members[i].IsOnline = isOnline
		}
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

		UNION

		SELECT d.id, d.title, d.status, d.user_id, u.name, l.linked_at
		FROM group_room_doc_links l
		JOIN documents d ON l.document_id = d.id
		JOIN users u ON d.user_id = u.id
		WHERE l.room_id = $1

		ORDER BY created_at ASC`, roomID)
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

// LinkDocToRoom — POST /api/v1/rooms/:id/docs/link
// Liên kết tài liệu có sẵn trong thư viện vào phòng (không cần upload lại)
func LinkDocToRoom(c *gin.Context) {
	userID := c.GetString("user_id")
	roomID := c.Param("id")

	var req struct {
		DocumentID string `json:"document_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": "VALIDATION_ERROR", "message": "document_id là bắt buộc"})
		return
	}

	// 1. Kiểm tra user là member active
	if !IsRoomMember(roomID, userID) {
		c.JSON(403, gin.H{"success": false, "error": "NOT_MEMBER", "message": "Bạn không phải thành viên của phòng này"})
		return
	}

	// 2. Kiểm tra tài liệu tồn tại và thuộc user, đã ready
	var docTitle, docStatus string
	err := config.DB.QueryRow(config.Ctx, `
		SELECT title, status FROM documents
		WHERE id = $1 AND user_id = $2`,
		req.DocumentID, userID).Scan(&docTitle, &docStatus)
	if err != nil {
		c.JSON(404, gin.H{"success": false, "error": "DOC_NOT_FOUND", "message": "Không tìm thấy tài liệu hoặc không có quyền"})
		return
	}
	if docStatus != "ready" {
		c.JSON(400, gin.H{"success": false, "error": "DOC_NOT_READY", "message": "Tài liệu chưa sẵn sàng (đang xử lý hoặc lỗi)"})
		return
	}

	// 3. Kiểm tra quota per-user (tối đa 3 tài liệu/người)
	var userDocCount int
	config.DB.QueryRow(config.Ctx, `
		SELECT COUNT(*) FROM (
			SELECT d.id FROM documents d WHERE d.room_id = $1 AND d.user_id = $2
			UNION
			SELECT l.document_id FROM group_room_doc_links l WHERE l.room_id = $1 AND l.linked_by = $2
		) sub`, roomID, userID).Scan(&userDocCount)
	if userDocCount >= 3 {
		c.JSON(403, gin.H{"success": false, "error": "ROOM_DOC_LIMIT", "message": "Tối đa 3 tài liệu/người trong một phòng."})
		return
	}

	// 4. Kiểm tra chưa được link vào phòng này
	var alreadyLinked bool
	config.DB.QueryRow(config.Ctx, `
		SELECT EXISTS(
			SELECT 1 FROM group_room_doc_links
			WHERE room_id = $1 AND document_id = $2
		)`, roomID, req.DocumentID).Scan(&alreadyLinked)
	if alreadyLinked {
		c.JSON(409, gin.H{"success": false, "error": "ALREADY_LINKED", "message": "Tài liệu đã có trong phòng này"})
		return
	}

	// 5. Insert link
	_, err = config.DB.Exec(config.Ctx, `
		INSERT INTO group_room_doc_links (room_id, document_id, linked_by)
		VALUES ($1, $2, $3)`, roomID, req.DocumentID, userID)
	if err != nil {
		log.Printf("❌ [LinkDocToRoom] Insert error: %v", err)
		c.JSON(500, gin.H{"success": false, "message": "Lỗi khi thêm tài liệu vào phòng"})
		return
	}

	// 6. Broadcast WS event
	var userName string
	config.DB.QueryRow(config.Ctx, `SELECT name FROM users WHERE id = $1`, userID).Scan(&userName)
	ws.RoomHubInstance.BroadcastToRoom(roomID, models.RoomEvent{
		Type:   "doc_linked",
		RoomID: roomID,
		UserID: userID,
		Payload: gin.H{
			"user_name": userName,
			"doc_name":  docTitle,
			"doc_id":    req.DocumentID,
		},
	})

	c.JSON(200, gin.H{"success": true, "data": gin.H{
		"doc_id": req.DocumentID,
		"title":  docTitle,
	}})
}

// UnlinkDocFromRoom — DELETE /api/v1/rooms/:id/docs/:doc_id
// Host hoặc người đã link tài liệu mới được gỡ
func UnlinkDocFromRoom(c *gin.Context) {
	userID := c.GetString("user_id")
	roomID := c.Param("id")
	docID := c.Param("doc_id")

	if !IsRoomMember(roomID, userID) {
		c.JSON(403, gin.H{"success": false, "message": "Bạn không phải thành viên phòng này"})
		return
	}

	// Kiểm tra host hoặc là người đã link tài liệu
	var isHost bool
	config.DB.QueryRow(config.Ctx, `SELECT is_host FROM group_room_members WHERE room_id=$1 AND user_id=$2 AND left_at IS NULL`, roomID, userID).Scan(&isHost)

	var linkedBy string
	config.DB.QueryRow(config.Ctx, `SELECT linked_by FROM group_room_doc_links WHERE room_id=$1 AND document_id=$2`, roomID, docID).Scan(&linkedBy)

	// Cũng cho phép owner của document uploaded vào room (không qua link)
	var docOwnerID string
	config.DB.QueryRow(config.Ctx, `SELECT user_id FROM documents WHERE id=$1 AND room_id=$2`, docID, roomID).Scan(&docOwnerID)

	if !isHost && linkedBy != userID && docOwnerID != userID {
		c.JSON(403, gin.H{"success": false, "message": "Chỉ host hoặc người thêm tài liệu mới có thể gỡ"})
		return
	}

	// Xóa link
	res, err := config.DB.Exec(config.Ctx, `DELETE FROM group_room_doc_links WHERE room_id=$1 AND document_id=$2`, roomID, docID)
	if err != nil || res.RowsAffected() == 0 {
		// Thử xóa doc upload trực tiếp vào room
		config.DB.Exec(config.Ctx, `UPDATE documents SET room_id=NULL WHERE id=$1 AND room_id=$2`, docID, roomID)
	}

	var userName string
	config.DB.QueryRow(config.Ctx, `SELECT name FROM users WHERE id=$1`, userID).Scan(&userName)
	ws.RoomHubInstance.BroadcastToRoom(roomID, models.RoomEvent{
		Type: "doc_unlinked", RoomID: roomID, UserID: userID,
		Payload: gin.H{"doc_id": docID, "user_name": userName},
	})

	c.JSON(200, gin.H{"success": true, "message": "Đã gỡ tài liệu khỏi phòng"})
}

// KickMember — POST /api/v1/rooms/:id/kick/:user_id
// Chỉ host mới được kick thành viên (không tự kick)
func KickMember(c *gin.Context) {
	hostID := c.GetString("user_id")
	roomID := c.Param("id")
	targetID := c.Param("user_id")

	if hostID == targetID {
		c.JSON(400, gin.H{"success": false, "message": "Không thể tự kick chính mình"})
		return
	}

	var isHost bool
	config.DB.QueryRow(config.Ctx, `SELECT is_host FROM group_room_members WHERE room_id=$1 AND user_id=$2 AND left_at IS NULL`, roomID, hostID).Scan(&isHost)
	if !isHost {
		c.JSON(403, gin.H{"success": false, "message": "Chỉ chủ phòng mới có quyền kick thành viên"})
		return
	}

	res, err := config.DB.Exec(config.Ctx, `
		UPDATE group_room_members SET left_at = NOW()
		WHERE room_id=$1 AND user_id=$2 AND left_at IS NULL`, roomID, targetID)
	if err != nil || res.RowsAffected() == 0 {
		c.JSON(404, gin.H{"success": false, "message": "Không tìm thấy thành viên"})
		return
	}

	var targetName string
	config.DB.QueryRow(config.Ctx, `SELECT name FROM users WHERE id=$1`, targetID).Scan(&targetName)

	ws.RoomHubInstance.BroadcastToRoom(roomID, models.RoomEvent{
		Type: "user_kicked", RoomID: roomID, UserID: targetID,
		Payload: gin.H{"user_id": targetID, "name": targetName},
	})

	log.Printf("[Room %s] Host %s kicked member %s", roomID, hostID, targetID)
	c.JSON(200, gin.H{"success": true, "message": fmt.Sprintf("Đã kick %s khỏi phòng", targetName)})
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
		SELECT grm.user_id, u.name, COALESCE(u.avatar_url, ''), grm.joined_at, grm.is_host,
		    (SELECT COUNT(*) FROM (
		        SELECT d.id FROM documents d WHERE d.room_id = $1 AND d.user_id = grm.user_id
		        UNION
		        SELECT l.document_id FROM group_room_doc_links l WHERE l.room_id = $1 AND l.linked_by = grm.user_id
		    ) doc_sub) AS actual_doc_count
		FROM group_room_members grm
		JOIN users u ON grm.user_id = u.id
		WHERE grm.room_id = $1 AND grm.left_at IS NULL
		ORDER BY grm.joined_at ASC`, roomID)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var m models.GroupRoomMember
			rows.Scan(&m.UserID, &m.Name, &m.AvatarURL, &m.JoinedAt, &m.IsHost, &m.DocCount)
			m.RoomID = roomID

			// Lấy LastSeen từ Redis
			if config.RedisClient != nil {
				val, err := config.RedisClient.Get(config.Ctx, fmt.Sprintf("room_last_seen:%s:%s", roomID, m.UserID)).Result()
				if err == nil {
					t, _ := time.Parse(time.RFC3339, val)
					m.LastSeen = &t
				}
			}

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
