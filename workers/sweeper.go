package workers

import (
	"fmt"
	"log"
	"mindex-backend/config"
	"mindex-backend/utils"
	"time"
)

const sweeperInterval = 1 * time.Minute

// StartSweeper chạy định kỳ để dọn dẹp tài liệu đã hết hạn
func StartSweeper() {
	log.Printf("🛠 Khởi chạy Sweeper (chu kỳ %s)...", sweeperInterval)

	RunSweeperNow()

	go func() {
		ticker := time.NewTicker(sweeperInterval)
		defer ticker.Stop()
		for range ticker.C {
			RunSweeperNow()
		}
	}()
}

func RunSweeperNow() (map[string]interface{}, error) {
	start := time.Now()

	// 1. Lấy danh sách các tài liệu sắp bị xóa để gửi thông báo
	rows, err := config.DB.Query(config.Ctx, `
		SELECT d.id, dr.user_id, d.title, d.is_public
		FROM documents d
		JOIN document_references dr ON dr.document_id = d.id
		WHERE d.expired_at IS NOT NULL AND d.expired_at < NOW()`)

	type delDoc struct {
		ID       string
		UserID   string
		Title    string
		IsPublic bool
	}
	var toDelete []delDoc
	shouldClearCommunity := false

	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var d delDoc
			if err := rows.Scan(&d.ID, &d.UserID, &d.Title, &d.IsPublic); err == nil {
				toDelete = append(toDelete, d)
				if d.IsPublic {
					shouldClearCommunity = true
				}
			}
		}
	}

	// 2. Thực hiện xóa hàng loạt
	// Xóa tài liệu cá nhân hết hạn
	resPriv, err := config.DB.Exec(config.Ctx, `
		DELETE FROM documents 
		WHERE expired_at IS NOT NULL AND expired_at < NOW() AND is_public = FALSE
	`)
	if err != nil {
		log.Println("Sweeper error (Private):", err)
	}

	// Đào thải tài liệu công cộng hết hạn
	resPub, err := config.DB.Exec(config.Ctx, `
		DELETE FROM documents 
		WHERE expired_at IS NOT NULL AND expired_at < NOW() AND is_public = TRUE
	`)
	if err != nil {
		log.Println("Sweeper error (Public):", err)
	}

	// 3. Gửi thông báo cho từng người dùng (Background)
	go func() {
		for _, d := range toDelete {
			utils.ClearUserCache("docs", d.UserID)
			utils.ClearUserCache("collections", d.UserID)
			utils.PublishNotification(
				d.UserID,
				"document_deleted",
				"Tài liệu đã được dọn dẹp",
				fmt.Sprintf("Tài liệu \"%s\" đã được dọn dẹp hệ thống sau khi hết hạn.", d.Title),
				map[string]string{"doc_id": d.ID},
			)
		}

		if shouldClearCommunity {
			utils.ClearCommunityCache()
		}
	}()

	// Document_chunks tự động bị xóa nhờ ON DELETE CASCADE
	// (Giải phóng dung lượng Vector Storage lớn nhất)

	log.Printf("🧹 [Sweeper] Đã dọn dẹp %d private docs, %d public docs", resPriv.RowsAffected(), resPub.RowsAffected())

	return map[string]interface{}{
		"deleted_private": resPriv.RowsAffected(),
		"deleted_public":  resPub.RowsAffected(),
		"duration_ms":     time.Since(start).Milliseconds(),
	}, nil
}
