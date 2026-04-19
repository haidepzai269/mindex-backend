package controllers

import (
	"encoding/json"
	"fmt"
	"log"
	"mindex-backend/config"
	"mindex-backend/utils"
	"time"

	"github.com/gin-gonic/gin"
)

type DocumentItem struct {
	ID         string     `json:"id"`
	Title      string     `json:"title"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	SharedAt   *time.Time `json:"shared_at"`
	ExpiredAt  *time.Time `json:"expired_at"`
	Pinned     bool       `json:"pinned"`
	IsPublic   bool       `json:"is_public"`
	ChunkCount int        `json:"chunk_count"`
}

// GetMyDocuments trả về danh sách tài liệu của người dùng hiện tại (Có Cache)
func GetMyDocuments(c *gin.Context) {
	userID := c.GetString("user_id")
	cacheKey := utils.GenerateUserCacheKey("docs", userID)

	// 1. Thử lấy từ Cache
	if cacheData := utils.GetCache(cacheKey); cacheData != "" {
		var docs []DocumentItem
		if err := json.Unmarshal([]byte(cacheData), &docs); err == nil {
			c.JSON(200, gin.H{
				"success": true,
				"data":    docs,
				"cached":  true,
			})
			return
		}
	}

	// 2. Nếu không có cache, truy vấn DB
	rows, err := config.DB.Query(config.Ctx, `
		SELECT d.id, d.title, d.status, d.created_at, d.shared_at, d.expired_at, dr.pinned, d.is_public,
		       (SELECT COUNT(*) FROM document_chunks WHERE document_id = d.id) as chunk_count
		FROM documents d
		JOIN document_references dr ON d.id = dr.document_id
		WHERE dr.user_id = $1
		ORDER BY COALESCE(d.shared_at, d.created_at) DESC, d.created_at DESC`,
		userID,
	)
	if err != nil {
		log.Printf("Error querying documents for user %s: %v", userID, err)
		c.JSON(500, gin.H{"success": false, "message": "Không thể tải danh sách tài liệu"})
		return
	}
	defer rows.Close()

	docs := []DocumentItem{}
	for rows.Next() {
		var d DocumentItem
		err := rows.Scan(
			&d.ID, 
			&d.Title, 
			&d.Status, 
			&d.CreatedAt, 
			&d.SharedAt,
			&d.ExpiredAt, 
			&d.Pinned, 
			&d.IsPublic,
			&d.ChunkCount,
		)
		if err != nil {
			log.Printf("Error scanning document row: %v", err)
			continue
		}

		docs = append(docs, d)
	}

	// 3. Lưu vào Cache (TTL 10 phút)
	if jsonData, err := json.Marshal(docs); err == nil {
		utils.SetCache(cacheKey, string(jsonData), 10*time.Minute)
	}

	c.JSON(200, gin.H{
		"success": true,
		"data":    docs,
	})
}

// GetDocumentDetail trả về thông tin chi tiết của một tài liệu cụ thể
func GetDocumentDetail(c *gin.Context) {
	userID := c.GetString("user_id")
	docID := c.Param("id")
	forkLinkID := c.Query("fork") // Lấy link_id từ query param ?fork=...

	// Nếu có yêu cầu fork, thực hiện auto-reference trước
	if forkLinkID != "" {
		var sharedDocID string
		var allowFork bool
		var expiredAt *time.Time

		err := config.DB.QueryRow(config.Ctx, `
			SELECT document_id, (settings->>'allow_fork')::boolean, expired_at 
			FROM shared_links WHERE id = $1`, forkLinkID).Scan(&sharedDocID, &allowFork, &expiredAt)

		if err == nil && sharedDocID == docID && allowFork {
			if expiredAt == nil || time.Now().Before(*expiredAt) {
				// Auto-insert vào document_references cho user hiện tại
				_, err = config.DB.Exec(config.Ctx, `
					INSERT INTO document_references (user_id, document_id) 
					VALUES ($1, $2) ON CONFLICT DO NOTHING`, userID, docID)
				if err != nil {
					log.Printf("⚠️ [FORK] Lỗi auto-reference cho user %s, doc %s: %v", userID, docID, err)
				} else {
					log.Printf("✅ [FORK] Đã tự động thêm tài liệu %s vào thư viện cho user %s via fork %s", docID, userID, forkLinkID)
				}
			}
		}
	}

	var d DocumentItem
	err := config.DB.QueryRow(config.Ctx, `
		SELECT d.id, d.title, d.status, d.created_at, d.shared_at, d.expired_at, COALESCE(dr.pinned, FALSE), d.is_public,
		       (SELECT COUNT(*) FROM document_chunks WHERE document_id = d.id) as chunk_count
		FROM documents d
		LEFT JOIN document_references dr ON d.id = dr.document_id AND dr.user_id = $2
		WHERE d.id = $1
		  AND (d.expired_at IS NULL OR d.expired_at > NOW())`,
		docID, userID,
	).Scan(
		&d.ID, 
		&d.Title, 
		&d.Status, 
		&d.CreatedAt, 
		&d.SharedAt,
		&d.ExpiredAt, 
		&d.Pinned, 
		&d.IsPublic,
		&d.ChunkCount,
	)

	if err != nil {
		log.Printf("Error querying document detail %s for user %s: %v", docID, userID, err)
		c.JSON(404, gin.H{"success": false, "message": "Tài liệu không tồn tại hoặc bạn không có quyền truy cập"})
		return
	}

	c.JSON(200, gin.H{
		"success": true,
		"data":    d,
	})
}

// TogglePinDocument ghim hoặc bỏ ghim tài liệu
func TogglePinDocument(c *gin.Context) {
	userID := c.GetString("user_id")
	docID := c.Param("id")

	var req struct {
		Pinned bool `json:"pinned"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "message": "Dữ liệu không hợp lệ"})
		return
	}

		// 1. Kiểm tra Quota nếu là hành động Ghim
		if req.Pinned {
			var pinnedCount int
			var tier string
			err := config.DB.QueryRow(config.Ctx, `
				SELECT 
					(SELECT COUNT(*) FROM document_references WHERE user_id = $1 AND pinned = TRUE),
					COALESCE(tier, 'FREE')
				FROM users WHERE id = $1`,
				userID,
			).Scan(&pinnedCount, &tier)

			if err != nil {
				c.JSON(500, gin.H{"success": false, "message": "Lỗi kiểm tra quota"})
				return
			}

			limit := 3
			if tier == "PRO" {
				limit = 5
			} else if tier == "ULTRA" {
				limit = 10
			}

			if pinnedCount >= limit {
				c.JSON(403, gin.H{
					"success": false,
					"error":   "PIN_QUOTA_EXCEEDED",
					"message": fmt.Sprintf("Bạn đã ghim tối đa %d tài liệu. Hãy bỏ ghim tài liệu cũ để tiếp tục.", limit),
				})
				return
			}
		}

	// 2. Cập nhật trạng thái Ghim trong references (Sử dụng UPSERT để đảm bảo)
	_, err := config.DB.Exec(config.Ctx, `
		INSERT INTO document_references (user_id, document_id, pinned)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id, document_id) 
		DO UPDATE SET pinned = EXCLUDED.pinned`,
		userID, docID, req.Pinned,
	)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Không thể cập nhật trạng thái ghim"})
		return
	}

	// 3. Cập nhật vòng đời tài liệu (expired_at)
	if req.Pinned {
		// Nếu ghim -> Xóa ngày hết hạn (Lưu vĩnh viễn)
		_, _ = config.DB.Exec(config.Ctx, `
			UPDATE documents SET expired_at = NULL WHERE id = $1`, 
			docID,
		)
	} else {
		// Nếu bỏ ghim -> Kiểm tra xem còn ai ghim tài liệu này không
		var othersPinned int
		config.DB.QueryRow(config.Ctx, `
			SELECT COUNT(*) FROM document_references 
			WHERE document_id = $1 AND pinned = TRUE`, 
			docID,
		).Scan(&othersPinned)

		if othersPinned == 0 {
			// Không còn ai ghim -> Đặt lại hạn 24h
			_, _ = config.DB.Exec(config.Ctx, `
				UPDATE documents 
				SET expired_at = NOW() + INTERVAL '24 hours' 
				WHERE id = $1`, 
				docID,
			)
		}
	}

	// 4. Xóa cache profile user để /auth/me lấy dữ liệu mới và xóa cache docs
	if config.RedisClient != nil {
		config.RedisClient.Del(config.Ctx, fmt.Sprintf("user:profile:%s", userID))
		utils.ClearUserCache("docs", userID)
		utils.ClearUserCache("collections", userID) // Ghim/bỏ ghim có thể ảnh hưởng đến collection preview
	}

	// 5. Gửi thông báo realtime qua SSE để cập nhật sidebar ngay lập tức
	_ = utils.PublishNotification(userID, "quota_update", "Cập nhật giới hạn", "Trạng thái ghim đã thay đổi", nil)

	c.JSON(200, gin.H{
		"success": true, 
		"message": map[bool]string{true: "Đã ghim tài liệu", false: "Đã bỏ ghim tài liệu"}[req.Pinned],
		"data": gin.H{"pinned": req.Pinned},
	})
}

// DeleteDocument xóa quyền truy cập tài liệu và dọn dẹp dữ liệu nếu cần
func DeleteDocument(c *gin.Context) {
	userID := c.GetString("user_id")
	docID := c.Param("id")

	// 1. Xóa tất cả chat history của user này liên quan đến document này
	_, err := config.DB.Exec(config.Ctx, `
		DELETE FROM chat_histories 
		WHERE user_id = $1 AND document_id = $2`,
		userID, docID,
	)
	if err != nil {
		log.Printf("Error deleting chat histories for user %s, doc %s: %v", userID, docID, err)
	}

	// 2. Xóa bản tham chiếu của user này đối với tài liệu
	_, err = config.DB.Exec(config.Ctx, `
		DELETE FROM document_references 
		WHERE user_id = $1 AND document_id = $2`,
		userID, docID,
	)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Không thể xóa liên kết tài liệu"})
		return
	}

	// 3. Kiểm tra xem còn bất kỳ ai tham chiếu đến tài liệu này không
	var refCount int
	err = config.DB.QueryRow(config.Ctx, `
		SELECT COUNT(*) FROM document_references WHERE document_id = $1`, 
		docID,
	).Scan(&refCount)

	if err == nil && refCount == 0 {
		// 4. Nếu không còn ai dùng -> Xóa bản ghi gốc trong documents
		// (ON DELETE CASCADE sẽ tự xóa document_chunks)
		_, err = config.DB.Exec(config.Ctx, `DELETE FROM documents WHERE id = $1`, docID)
		if err != nil {
			log.Printf("Error deleting root document %s: %v", docID, err)
		} else {
			log.Printf("🧹 [Cleanup] Root document %s and its chunks have been fully deleted.", docID)
		}
	}

	// 5. Xóa cache và gửi thông báo realtime
	if config.RedisClient != nil {
		config.RedisClient.Del(config.Ctx, fmt.Sprintf("user:profile:%s", userID))
		utils.ClearUserCache("docs", userID)
		utils.ClearUserCache("collections", userID)
		utils.ClearCommunityCache()
	}
	_ = utils.PublishNotification(userID, "quota_update", "Cập nhật dữ liệu", "Tài liệu đã được xóa", nil)

	c.JSON(200, gin.H{
		"success": true, 
		"message": "Đã xóa tài liệu khỏi thư viện của bạn",
	})
}
// UpdateDocumentPersona cập nhật lĩnh vực của tài liệu (manual override)
func UpdateDocumentPersona(c *gin.Context) {
	userID := c.GetString("user_id")
	docID := c.Param("id")

	var req struct {
		Persona string `json:"persona" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "message": "Thông tin không hợp lệ"})
		return
	}

	// Chỉ cho phép chủ sở hữu cập nhật
	result, err := config.DB.Exec(config.Ctx, `
		UPDATE documents d
		SET creator_persona = $1
		FROM document_references dr
		WHERE d.id = dr.document_id AND d.id = $2 AND dr.user_id = $3 AND dr.is_owner = TRUE`,
		req.Persona, docID, userID,
	)

	if err != nil {
		log.Printf("❌ [UpdatePersona] Lỗi DB: %v", err)
		c.JSON(500, gin.H{"success": false, "message": "Không thể cập nhật lĩnh vực"})
		return
	}

	if result.RowsAffected() == 0 {
		c.JSON(403, gin.H{"success": false, "message": "Bạn không có quyền chỉnh sửa tài liệu này hoặc tài liệu không tồn tại"})
		return
	}

	utils.ClearUserCache("docs", userID)
	utils.ClearCommunityCache()
	c.JSON(200, gin.H{"success": true, "message": "Đã cập nhật lĩnh vực tài liệu"})
}
