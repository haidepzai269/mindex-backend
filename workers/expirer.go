package workers

import (
	"log"
	"mindex-backend/config"
	"mindex-backend/utils"
	"time"
)

// StartExpirer chạy định kỳ mỗi 5 phút để thông báo tài liệu vừa hết hạn
func StartExpirer() {
	log.Println("🛠 Khởi chạy Worker Expirer (Mỗi 5 phút)...")

	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		for range ticker.C {
			RunExpirerNow()
		}
	}()
}

func RunExpirerNow() {
	// Tìm các tài liệu đã hết hạn nhưng chưa được thông báo
	rows, err := config.DB.Query(config.Ctx, `
		SELECT id, user_id, title, expired_at 
		FROM documents 
		WHERE expired_at IS NOT NULL 
		  AND expired_at < NOW() 
		  AND expiration_notified = FALSE
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
