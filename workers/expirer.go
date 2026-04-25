package workers

import (
	"log"
	"mindex-backend/config"
	"mindex-backend/utils"
	"time"
)

// StartExpirer chạy định kỳ để thông báo tài liệu vừa hết hạn
func StartExpirer() {
	log.Println("🛠 Khởi chạy Worker Expirer (chu kỳ 1 phút)...")

	RunExpirerNow()

	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			RunExpirerNow()
		}
	}()
}

func RunExpirerNow() {
	// Tìm các tài liệu đã hết hạn nhưng chưa được thông báo cho các user đang tham chiếu
	rows, err := config.DB.Query(config.Ctx, `
		SELECT d.id, dr.user_id, d.title, d.expired_at
		FROM documents d
		JOIN document_references dr ON dr.document_id = d.id
		WHERE d.expired_at IS NOT NULL
		  AND d.expired_at < NOW()
		  AND d.expiration_notified = FALSE
		LIMIT 100`)

	if err != nil {
		log.Printf("❌ [Expirer] Lỗi truy vấn: %v", err)
		return
	}
	defer rows.Close()

	type expDoc struct {
		ID        string
		UserID    string
		Title     string
		ExpiredAt time.Time
	}
	var expiredList []expDoc

	for rows.Next() {
		var d expDoc
		if err := rows.Scan(&d.ID, &d.UserID, &d.Title, &d.ExpiredAt); err == nil {
			expiredList = append(expiredList, d)
		}
	}

	for _, d := range expiredList {
		// Gửi thông báo
		err := utils.PublishNotification(
			d.UserID,
			"document_expired",
			"Tài liệu đã hết hạn",
			"Tài liệu \""+d.Title+"\" đã hết hạn vào lúc "+d.ExpiredAt.Format("15:04 02/01/2006")+". Hệ thống sẽ dọn dẹp tài liệu này sớm.",
			map[string]interface{}{
				"doc_id":     d.ID,
				"expired_at": d.ExpiredAt,
				"sweep_at":   "3:00 AM", // Thông tin cứng từ Sweeper
			},
		)

		if err == nil {
			// Đánh dấu đã thông báo để không lặp lại
			config.DB.Exec(config.Ctx, `UPDATE documents SET expiration_notified = TRUE WHERE id = $1`, d.ID)
		}
	}

	if len(expiredList) > 0 {
		log.Printf("🔔 [Expirer] Đã gửi %d thông báo hết hạn", len(expiredList))
	}
}
