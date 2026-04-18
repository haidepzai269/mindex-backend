package controllers

import (
	"fmt"
	"mindex-backend/config"
	"mindex-backend/models"

	"github.com/gin-gonic/gin"
)

// CreateCollection tạo mới một bộ tài liệu
func CreateCollection(c *gin.Context) {
	userID := c.GetString("user_id")
	var req struct {
		Name        string   `json:"name" binding:"required,max=100"`
		Description string   `json:"description"`
		Emoji       string   `json:"emoji"`
		DocumentIDs []string `json:"document_ids"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": "VALIDATION_ERROR", "message": "Dữ liệu không hợp lệ"})
		return
	}

	// 1. Kiểm tra Quota (Tối đa 10 bộ)
	var count int
	err := config.DB.QueryRow(config.Ctx, `SELECT COUNT(*) FROM collections WHERE user_id=$1`, userID).Scan(&count)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Lỗi kiểm tra quota"})
		return
	}
	if count >= 10 {
		c.JSON(403, gin.H{"success": false, "error": "COLLECTION_QUOTA_EXCEEDED", "message": "Tối đa 10 bộ tài liệu. Xóa bộ cũ để tạo mới."})
		return
	}

	// 2. Validate documents (Tối đa 5 file, thuộc về user và status=ready)
	if len(req.DocumentIDs) > 5 {
		c.JSON(400, gin.H{"success": false, "error": "TOO_MANY_DOCS", "message": "Tối đa 5 tài liệu một bộ"})
		return
	}

	if len(req.DocumentIDs) > 0 {
		if err := validateDocumentOwnership(userID, req.DocumentIDs); err != nil {
			c.JSON(422, gin.H{"success": false, "error": "INVALID_DOCUMENTS", "message": err.Error()})
			return
		}
	}

	emoji := req.Emoji
	if emoji == "" {
		emoji = "📁"
	}

	// 3. Thực hiện tạo trong Transaction
	tx, err := config.DB.Begin(config.Ctx)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Lỗi khởi tạo giao diện DB"})
		return
	}
	defer tx.Rollback(config.Ctx)

	var colID string
	err = tx.QueryRow(config.Ctx, `
		INSERT INTO collections (user_id, name, description, emoji) 
		VALUES ($1, $2, $3, $4) RETURNING id`,
		userID, req.Name, req.Description, emoji).Scan(&colID)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Không thể tạo bộ tài liệu"})
		return
	}

	for i, docID := range req.DocumentIDs {
		_, err = tx.Exec(config.Ctx, `
			INSERT INTO collection_documents (collection_id, document_id, display_order) 
			VALUES ($1, $2, $3)`, colID, docID, i)
		if err != nil {
			c.JSON(500, gin.H{"success": false, "message": "Lỗi liên kết tài liệu"})
			return
		}
	}

	if err := tx.Commit(config.Ctx); err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Lỗi lưu dữ liệu"})
		return
	}

	// 4. Trả về thông tin đầy đủ
	collection := getCollectionDetailHelper(colID, userID)
	c.JSON(201, gin.H{"success": true, "data": collection})
}

// ListCollections lấy danh sách bộ tài liệu của user
func ListCollections(c *gin.Context) {
	userID := c.GetString("user_id")

	rows, err := config.DB.Query(config.Ctx, `
		SELECT c.id, c.name, c.emoji, c.description, c.doc_count, c.last_chat_at, c.created_at,
		       (SELECT MIN(d.expired_at) 
		        FROM documents d 
		        JOIN collection_documents cd ON d.id = cd.document_id 
		        JOIN document_references dr ON d.id = dr.document_id AND dr.user_id = c.user_id
		        WHERE cd.collection_id = c.id AND dr.pinned = false) as expires_at
		FROM collections c
		WHERE c.user_id = $1 
		  AND NOT EXISTS (
		      SELECT 1 FROM documents d 
		      JOIN collection_documents cd ON d.id = cd.document_id 
		      JOIN document_references dr ON d.id = dr.document_id AND dr.user_id = c.user_id
		      WHERE cd.collection_id = c.id AND d.expired_at < NOW() AND dr.pinned = false
		  )
		ORDER BY c.created_at DESC`, userID)
	
	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Lỗi truy vấn danh sách"})
		return
	}
	defer rows.Close()

	collections := []models.Collection{}
	for rows.Next() {
		var col models.Collection
		err := rows.Scan(&col.ID, &col.Name, &col.Emoji, &col.Description, &col.DocCount, &col.LastChatAt, &col.CreatedAt, &col.ExpiresAt)
		if err != nil {
			continue
		}
		
		// Lấy preview 3 tài liệu đầu tiên
		docRows, _ := config.DB.Query(config.Ctx, `
			SELECT d.title, d.status
			FROM documents d
			JOIN collection_documents cd ON d.id = cd.document_id
			WHERE cd.collection_id = $1
			ORDER BY cd.display_order ASC LIMIT 3`, col.ID)
		
		previews := []models.Document{}
		for docRows.Next() {
			var d models.Document
			docRows.Scan(&d.Title, &d.Status)
			previews = append(previews, d)
		}
		docRows.Close()
		col.Documents = previews
		
		collections = append(collections, col)
	}

	c.JSON(200, gin.H{"success": true, "data": collections})
}

// GetCollectionDetail chi tiết bộ tài liệu và các file bên trong
func GetCollectionDetail(c *gin.Context) {
	userID := c.GetString("user_id")
	colID := c.Param("id")

	collection := getCollectionDetailHelper(colID, userID)
	if collection == nil {
		c.JSON(404, gin.H{"success": false, "message": "Không tìm thấy bộ tài liệu"})
		return
	}

	c.JSON(200, gin.H{"success": true, "data": collection})
}

// UpdateCollection cập nhật thông tin bộ
func UpdateCollection(c *gin.Context) {
	userID := c.GetString("user_id")
	colID := c.Param("id")
	var req struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Emoji       string   `json:"emoji"`
		DocumentIDs []string `json:"document_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "message": "Dữ liệu không hợp lệ"})
		return
	}

	// Bắt đầu Transaction để cập nhật cả thông tin và danh sách file
	tx, err := config.DB.Begin(config.Ctx)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Lỗi kết nối DB"})
		return
	}
	defer tx.Rollback(config.Ctx)

	// 1. Cập nhật thông tin cơ bản
	_, err = tx.Exec(config.Ctx, `
		UPDATE collections 
		SET name = COALESCE(NULLIF($1, ''), name), 
		    description = $2, 
		    emoji = COALESCE(NULLIF($3, ''), emoji),
		    updated_at = NOW()
		WHERE id = $4 AND user_id = $5`,
		req.Name, req.Description, req.Emoji, colID, userID)

	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Không thể cập nhật thông tin bộ"})
		return
	}

	// 2. Nếu có gửi danh sách DocumentIDs, thực hiện đồng bộ lại
	if req.DocumentIDs != nil {
		// Validate số lượng
		if len(req.DocumentIDs) > 5 {
			c.JSON(400, gin.H{"success": false, "message": "Tối đa 5 tài liệu một bộ"})
			return
		}

		// Xóa liên kết cũ
		_, err = tx.Exec(config.Ctx, `DELETE FROM collection_documents WHERE collection_id = $1`, colID)
		if err != nil {
			c.JSON(500, gin.H{"success": false, "message": "Lỗi khi làm mới danh sách tài liệu"})
			return
		}

		// Chèn liên kết mới
		for i, docID := range req.DocumentIDs {
			_, err = tx.Exec(config.Ctx, `
				INSERT INTO collection_documents (collection_id, document_id, display_order) 
				VALUES ($1, $2, $3)`, colID, docID, i)
			if err != nil {
				c.JSON(500, gin.H{"success": false, "message": "Lỗi khi lưu danh sách tài liệu mới"})
				return
			}
		}
	}

	if err := tx.Commit(config.Ctx); err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Lỗi lưu dữ liệu cuối cùng"})
		return
	}

	c.JSON(200, gin.H{"success": true, "message": "Đã cập nhật bộ tài liệu thành công"})
}

// DeleteCollection xóa bộ tài liệu
func DeleteCollection(c *gin.Context) {
	userID := c.GetString("user_id")
	colID := c.Param("id")

	_, err := config.DB.Exec(config.Ctx, `DELETE FROM collections WHERE id = $1 AND user_id = $2`, colID, userID)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Không thể xóa bộ tài liệu"})
		return
	}

	c.JSON(200, gin.H{"success": true, "message": "Đã xóa bộ tài liệu"})
}

// AddDocumentToCollection thêm file vào bộ
func AddDocumentToCollection(c *gin.Context) {
	userID := c.GetString("user_id")
	colID := c.Param("id")
	var req struct {
		DocumentIDs []string `json:"document_ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "message": "Thiếu danh sách file"})
		return
	}

	// Kiểm tra quota hiện tại của bộ
	var currentCount int
	config.DB.QueryRow(config.Ctx, `SELECT doc_count FROM collections WHERE id = $1 AND user_id = $2`, colID, userID).Scan(&currentCount)
	if currentCount + len(req.DocumentIDs) > 5 {
		c.JSON(400, gin.H{"success": false, "message": "Bộ tài liệu chỉ chứa tối đa 5 file"})
		return
	}

	// Validate files
	if err := validateDocumentOwnership(userID, req.DocumentIDs); err != nil {
		c.JSON(422, gin.H{"success": false, "message": err.Error()})
		return
	}

	for _, docID := range req.DocumentIDs {
		_, _ = config.DB.Exec(config.Ctx, `
			INSERT INTO collection_documents (collection_id, document_id) 
			VALUES ($1, $2) ON CONFLICT DO NOTHING`, colID, docID)
	}

	c.JSON(200, gin.H{"success": true, "message": "Đã thêm tài liệu vào bộ"})
}

// RemoveDocumentFromCollection xóa file khỏi bộ
func RemoveDocumentFromCollection(c *gin.Context) {
	userID := c.GetString("user_id")
	colID := c.Param("id")
	docID := c.Param("doc_id")

	// Xác thực quyền sở hữu collection
	var exists bool
	config.DB.QueryRow(config.Ctx, `SELECT EXISTS(SELECT 1 FROM collections WHERE id=$1 AND user_id=$2)`, colID, userID).Scan(&exists)
	if !exists {
		c.JSON(403, gin.H{"success": false, "message": "Bạn không có quyền chỉnh sửa bộ tài liệu này"})
		return
	}

	_, err := config.DB.Exec(config.Ctx, `
		DELETE FROM collection_documents WHERE collection_id = $1 AND document_id = $2`, colID, docID)
	
	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Không thể gỡ tài liệu khỏi bộ"})
		return
	}

	c.JSON(200, gin.H{"success": true, "message": "Đã gỡ tài liệu khỏi bộ"})
}

// Helpers

func validateDocumentOwnership(userID string, docIDs []string) error {
	for _, id := range docIDs {
		var exists bool
		err := config.DB.QueryRow(config.Ctx, `
			SELECT EXISTS(
				SELECT 1 FROM document_references 
				WHERE user_id = $1 AND document_id = $2
			)`, userID, id).Scan(&exists)
		
		if err != nil || !exists {
			return fmt.Errorf("Tài liệu %s không tồn tại hoặc không thuộc về bạn", id)
		}

		var status string
		config.DB.QueryRow(config.Ctx, `SELECT status FROM documents WHERE id = $1`, id).Scan(&status)
		if status != "ready" {
			return fmt.Errorf("Tài liệu %s đang xử lý hoặc gặp lỗi, chưa thể dùng", id)
		}
	}
	return nil
}

func getCollectionDetailHelper(colID, userID string) *models.Collection {
	var col models.Collection
	err := config.DB.QueryRow(config.Ctx, `
		SELECT c.id, c.name, c.emoji, c.description, c.doc_count, c.last_chat_at, c.created_at,
		       (SELECT MIN(d.expired_at) 
		        FROM documents d 
		        JOIN collection_documents cd ON d.id = cd.document_id 
		        JOIN document_references dr ON d.id = dr.document_id AND dr.user_id = c.user_id
		        WHERE cd.collection_id = c.id AND dr.pinned = false) as expires_at
		FROM collections c 
		WHERE c.id = $1 AND c.user_id = $2`, colID, userID).Scan(
		&col.ID, &col.Name, &col.Emoji, &col.Description, &col.DocCount, &col.LastChatAt, &col.CreatedAt, &col.ExpiresAt)
	
	if err != nil {
		return nil
	}

	docRows, _ := config.DB.Query(config.Ctx, `
		SELECT d.id, d.title, d.status, d.expired_at, dr.pinned,
		       (SELECT COUNT(*) FROM document_chunks WHERE document_id = d.id) as chunk_count
		FROM documents d
		JOIN collection_documents cd ON d.id = cd.document_id
		JOIN document_references dr ON d.id = dr.document_id AND dr.user_id = $2
		WHERE cd.collection_id = $1
		ORDER BY cd.display_order ASC`, colID, userID)
	
	defer docRows.Close()

	docs := []models.Document{}
	for docRows.Next() {
		var d models.Document
		docRows.Scan(&d.ID, &d.Title, &d.Status, &d.ExpiredAt, &d.Pinned, &d.ChunkCount)
		docs = append(docs, d)
	}
	col.Documents = docs
	return &col
}
