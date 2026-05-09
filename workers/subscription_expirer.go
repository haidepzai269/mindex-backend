package workers

import (
	"log"
	"mindex-backend/config"
	"time"
)

// StartSubscriptionExpirer chạy cron daily lúc 2AM — downgrade user hết hạn tier về FREE
func StartSubscriptionExpirer() {
	go func() {
		for {
			now := time.Now()
			// Tính thời điểm 2AM ngày hôm sau
			next := time.Date(now.Year(), now.Month(), now.Day()+1, 2, 0, 0, 0, now.Location())
			time.Sleep(time.Until(next))

			runSubscriptionExpiry()
		}
	}()
	log.Println("✅ Subscription Expirer started (runs daily at 2AM)")
}

func runSubscriptionExpiry() {
	res, err := config.DB.Exec(config.Ctx, `
		UPDATE users
		SET tier = 'FREE', tier_expires_at = NULL
		WHERE tier != 'FREE'
		  AND tier_expires_at IS NOT NULL
		  AND tier_expires_at < NOW()`)
	if err != nil {
		log.Printf("❌ [SubscriptionExpirer] Error: %v", err)
		return
	}
	log.Printf("🔄 [SubscriptionExpirer] Downgraded %d expired subscriptions to FREE", res.RowsAffected())
}
