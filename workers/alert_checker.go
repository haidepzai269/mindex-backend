package workers

import (
	"log"
	"mindex-backend/controllers"
	"time"
)

// StartAlertChecker khởi động background job kiểm tra chất lượng AI mỗi 30 phút
// Gọi hàm này từ main.go sau khi DB đã sẵn sàng
func StartAlertChecker() {
	go func() {
		log.Println("🔔 [AlertChecker] Background quality alert checker started (interval: 30 min)")
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				log.Println("🔍 [AlertChecker] Running quality check...")
				controllers.CheckAndSendQualityAlert()
			}
		}
	}()
}
