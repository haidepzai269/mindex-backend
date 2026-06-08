package workers

import (
	"fmt"
	"log"
	"mindex-backend/config"
	"mindex-backend/utils"
	"time"
)

const sweeperInterval = 1 * time.Hour

const (
	tokenUsageRetention     = "90 days"
	aiLogRetention          = "180 days"
	closedRoomRetention     = "180 days"
	roomMsgRetention        = "180 days"
	chatSoftDeleteRetention = "90 days"
	readNotifRetention      = "30 days"
	unreadNotifRetention    = "90 days"
)

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
	// (Chỉ doc còn references - soft-deleted docs không có references nên skip)
	rows, err := config.DB.Query(config.Ctx, `
		SELECT d.id, dr.user_id, d.title, d.is_public, d.cloudinary_public_id
		FROM documents d
		JOIN document_references dr ON dr.document_id = d.id
		WHERE d.expired_at IS NOT NULL AND d.expired_at < NOW() AND d.deleted_at IS NULL`)

	type delDoc struct {
		ID       string
		UserID   string
		Title    string
		IsPublic bool
		PublicID *string
	}
	var toDelete []delDoc
	shouldClearCommunity := false

	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var d delDoc
			if err := rows.Scan(&d.ID, &d.UserID, &d.Title, &d.IsPublic, &d.PublicID); err == nil {
				toDelete = append(toDelete, d)
				if d.IsPublic {
					shouldClearCommunity = true
				}
			}
		}
	}

	// 2. Thực hiện xóa hàng loạt
	// Xóa tài liệu cá nhân hết hạn (bỏ qua soft-deleted - chúng có pipeline riêng)
	resPriv, err := config.DB.Exec(config.Ctx, `
		DELETE FROM documents
		WHERE expired_at IS NOT NULL AND expired_at < NOW()
		  AND is_public = FALSE AND deleted_at IS NULL
	`)
	if err != nil {
		log.Println("Sweeper error (Private):", err)
	}

	// Đào thải tài liệu công cộng hết hạn
	resPub, err := config.DB.Exec(config.Ctx, `
		DELETE FROM documents
		WHERE expired_at IS NOT NULL AND expired_at < NOW()
		  AND is_public = TRUE AND deleted_at IS NULL
	`)
	if err != nil {
		log.Println("Sweeper error (Public):", err)
	}

	// Hard-delete các tài liệu soft-deleted sau 7 ngày, kèm dọn Cloudinary
	resSoftDel, softDelDocs := sweepSoftDeletedDocuments()
	_ = resSoftDel

	retentionStats := runRetentionCleanup()

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
			if d.PublicID != nil && *d.PublicID != "" {
				if err := utils.DestroyRawFromCloudinary(*d.PublicID); err != nil {
					log.Printf("[Sweeper] Cloudinary cleanup failed for %s: %v", d.ID, err)
				}
			}
		}

		if shouldClearCommunity {
			utils.ClearCommunityCache()
		}
	}()

	// Document_chunks tự động bị xóa nhờ ON DELETE CASCADE
	// (Giải phóng dung lượng Vector Storage lớn nhất)

	log.Printf("🧹 [Sweeper] Đã dọn dẹp %d private docs, %d public docs, %d soft-deleted docs",
		resPriv.RowsAffected(), resPub.RowsAffected(), len(softDelDocs))

	return map[string]interface{}{
		"deleted_private": resPriv.RowsAffected(),
		"deleted_public":  resPub.RowsAffected(),
		"retention":       retentionStats,
		"duration_ms":     time.Since(start).Milliseconds(),
	}, nil
}

type softDelDoc struct {
	ID       string
	PublicID *string
}

// sweepSoftDeletedDocuments hard-deletes docs đã soft-delete quá 7 ngày và dọn Cloudinary.
func sweepSoftDeletedDocuments() (int64, []softDelDoc) {
	rows, err := config.DB.Query(config.Ctx, `
		SELECT id, cloudinary_public_id FROM documents
		WHERE deleted_at IS NOT NULL AND deleted_at < NOW() - INTERVAL '7 days'`)
	if err != nil {
		log.Printf("[Sweeper] soft-delete query failed: %v", err)
		return 0, nil
	}
	defer rows.Close()

	var docs []softDelDoc
	for rows.Next() {
		var d softDelDoc
		if rows.Scan(&d.ID, &d.PublicID) == nil {
			docs = append(docs, d)
		}
	}

	if len(docs) == 0 {
		return 0, nil
	}

	ids := make([]string, len(docs))
	for i, d := range docs {
		ids[i] = d.ID
	}

	res, err := config.DB.Exec(config.Ctx,
		`DELETE FROM documents WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		log.Printf("[Sweeper] soft-delete hard-delete failed: %v", err)
		return 0, nil
	}

	go func() {
		for _, d := range docs {
			if d.PublicID != nil && *d.PublicID != "" {
				if err := utils.DestroyRawFromCloudinary(*d.PublicID); err != nil {
					log.Printf("[Sweeper] Cloudinary cleanup failed for soft-deleted %s: %v", d.ID, err)
				}
			}
		}
	}()

	return res.RowsAffected(), docs
}

func runRetentionCleanup() map[string]int64 {
	stats := map[string]int64{}

	cleanups := []struct {
		name string
		sql  string
	}{
		{
			name: "token_usage_logs",
			sql:  "DELETE FROM token_usage_logs WHERE created_at < NOW() - INTERVAL '" + tokenUsageRetention + "'",
		},
		{
			name: "ai_response_logs",
			sql:  "DELETE FROM ai_response_logs WHERE created_at < NOW() - INTERVAL '" + aiLogRetention + "'",
		},
		{
			name: "ai_response_logs_orphan_documents",
			sql: `
				DELETE FROM ai_response_logs l
				WHERE l.document_id IS NOT NULL
				  AND NOT EXISTS (
					SELECT 1 FROM documents d WHERE d.id::text = l.document_id
				  )`,
		},
		{
			name: "ai_response_logs_orphan_collections",
			sql: `
				DELETE FROM ai_response_logs l
				WHERE l.collection_id IS NOT NULL
				  AND NOT EXISTS (
					SELECT 1 FROM collections c WHERE c.id::text = l.collection_id
				  )`,
		},
		{
			name: "ai_response_logs_orphan_users",
			sql: `
				DELETE FROM ai_response_logs l
				WHERE NOT EXISTS (
					SELECT 1 FROM users u WHERE u.id::text = l.user_id
				)`,
		},
		{
			name: "room_messages",
			sql: `
				DELETE FROM room_messages rm
				USING group_rooms gr
				WHERE rm.room_id = gr.id
				  AND gr.closed_at IS NOT NULL
				  AND gr.closed_at < NOW() - INTERVAL '` + roomMsgRetention + `'`,
		},
		{
			name: "closed_room_documents",
			sql: `
				DELETE FROM documents d
				USING group_rooms gr
				WHERE d.room_id = gr.id
				  AND gr.closed_at IS NOT NULL
				  AND gr.closed_at < NOW() - INTERVAL '` + closedRoomRetention + `'`,
		},
		{
			name: "closed_rooms",
			sql: `
				DELETE FROM group_rooms
				WHERE closed_at IS NOT NULL
				  AND closed_at < NOW() - INTERVAL '` + closedRoomRetention + `'`,
		},
		{
			name: "notifications",
			sql: `
				DELETE FROM notifications
				WHERE (read_at IS NOT NULL AND read_at < NOW() - INTERVAL '` + readNotifRetention + `')
				   OR (read_at IS NULL AND created_at < NOW() - INTERVAL '` + unreadNotifRetention + `')`,
		},
		{
			name: "chat_histories_soft_deleted_messages",
			sql: `
				WITH rewritten AS (
					SELECT
						session_id,
						COALESCE((
							SELECT jsonb_agg(elem)
							FROM jsonb_array_elements(COALESCE(full_messages, '[]'::jsonb)) AS elem
							WHERE NOT (
								elem ? 'deleted_at'
								AND NULLIF(elem->>'deleted_at', '') IS NOT NULL
								AND (elem->>'deleted_at')::timestamptz < NOW() - INTERVAL '` + chatSoftDeleteRetention + `'
							)
						), '[]'::jsonb) AS next_messages
					FROM chat_histories
					WHERE EXISTS (
						SELECT 1
						FROM jsonb_array_elements(COALESCE(full_messages, '[]'::jsonb)) AS elem
						WHERE elem ? 'deleted_at'
						  AND NULLIF(elem->>'deleted_at', '') IS NOT NULL
						  AND (elem->>'deleted_at')::timestamptz < NOW() - INTERVAL '` + chatSoftDeleteRetention + `'
					)
				)
				UPDATE chat_histories ch
				SET full_messages = rewritten.next_messages,
				    message_count = jsonb_array_length(rewritten.next_messages)
				FROM rewritten
				WHERE ch.session_id = rewritten.session_id`,
		},
		{
			name: "shared_links",
			sql:  "DELETE FROM shared_links WHERE expired_at IS NOT NULL AND expired_at < NOW()",
		},
	}

	for _, cleanup := range cleanups {
		res, err := config.DB.Exec(config.Ctx, cleanup.sql)
		if err != nil {
			log.Printf("[Sweeper] Retention cleanup failed for %s: %v", cleanup.name, err)
			continue
		}
		stats[cleanup.name] = res.RowsAffected()
	}

	return stats
}
