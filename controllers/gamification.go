package controllers

import (
	"fmt"
	"log"
	"mindex-backend/config"
	"time"

	"github.com/gin-gonic/gin"
)

// Định nghĩa các badge có thể đạt được
var badgeDefs = []struct {
	ID          string
	Name        string
	Description string
	Emoji       string
	CheckFn     func(userID string) bool
}{
	{
		ID: "first_upload", Name: "Người tiên phong", Emoji: "🚀",
		Description: "Upload tài liệu đầu tiên",
		CheckFn: func(uid string) bool {
			var n int
			config.DB.QueryRow(config.Ctx, `SELECT COUNT(*) FROM document_references WHERE user_id=$1 AND is_owner=TRUE`, uid).Scan(&n)
			return n >= 1
		},
	},
	{
		ID: "first_quiz", Name: "Bước đầu kiểm tra", Emoji: "📝",
		Description: "Hoàn thành quiz đầu tiên",
		CheckFn: func(uid string) bool {
			var n int
			config.DB.QueryRow(config.Ctx, `SELECT COUNT(*) FROM quiz_attempts WHERE user_id=$1`, uid).Scan(&n)
			return n >= 1
		},
	},
	{
		ID: "quiz_master", Name: "Quiz Master", Emoji: "🏆",
		Description: "Đạt điểm 100 trong một bài quiz",
		CheckFn: func(uid string) bool {
			var n int
			config.DB.QueryRow(config.Ctx, `SELECT COUNT(*) FROM quiz_attempts WHERE user_id=$1 AND score=100`, uid).Scan(&n)
			return n >= 1
		},
	},
	{
		ID: "flashcard_hero", Name: "Flashcard Hero", Emoji: "🃏",
		Description: "Tạo và ôn luyện 100 flashcard",
		CheckFn: func(uid string) bool {
			var n int
			config.DB.QueryRow(config.Ctx, `SELECT COUNT(*) FROM flashcards f JOIN flashcard_sets fs ON fs.id=f.set_id WHERE fs.user_id=$1 AND f.remembered=TRUE`, uid).Scan(&n)
			return n >= 100
		},
	},
	{
		ID: "community_contributor", Name: "Nhà đóng góp", Emoji: "🌟",
		Description: "Chia sẻ tài liệu lên cộng đồng",
		CheckFn: func(uid string) bool {
			var n int
			config.DB.QueryRow(config.Ctx, `SELECT COUNT(*) FROM documents WHERE user_id=$1 AND is_public=TRUE`, uid).Scan(&n)
			return n >= 1
		},
	},
	{
		ID: "doc_collector", Name: "Người sưu tầm", Emoji: "📚",
		Description: "Có 10 tài liệu trong thư viện",
		CheckFn: func(uid string) bool {
			var n int
			config.DB.QueryRow(config.Ctx, `SELECT COUNT(*) FROM document_references WHERE user_id=$1`, uid).Scan(&n)
			return n >= 10
		},
	},
	{
		ID: "week_streak", Name: "Kiên trì 7 ngày", Emoji: "🔥",
		Description: "Học liên tục 7 ngày",
		CheckFn: func(uid string) bool {
			var n int
			config.DB.QueryRow(config.Ctx, `SELECT current_streak FROM user_streaks WHERE user_id=$1`, uid).Scan(&n)
			return n >= 7
		},
	},
}

// GetBadges trả về danh sách badge của user (đã đạt + chưa đạt)
// GET /gamification/badges
func GetBadges(c *gin.Context) {
	userID := c.GetString("user_id")

	// Lấy badges đã đạt từ DB
	earned := map[string]time.Time{}
	rows, err := config.DB.Query(config.Ctx, `SELECT badge_id, earned_at FROM user_badges WHERE user_id=$1`, userID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var bid string
			var t time.Time
			if scanErr := rows.Scan(&bid, &t); scanErr == nil {
				earned[bid] = t
			}
		}
	}

	type BadgeResult struct {
		ID          string     `json:"id"`
		Name        string     `json:"name"`
		Description string     `json:"description"`
		Emoji       string     `json:"emoji"`
		Earned      bool       `json:"earned"`
		EarnedAt    *time.Time `json:"earned_at,omitempty"`
	}

	var result []BadgeResult
	for _, b := range badgeDefs {
		br := BadgeResult{ID: b.ID, Name: b.Name, Description: b.Description, Emoji: b.Emoji}
		if t, ok := earned[b.ID]; ok {
			br.Earned = true
			br.EarnedAt = &t
		}
		result = append(result, br)
	}

	c.JSON(200, gin.H{"success": true, "data": result})
}

// cacheKeyEarnedBadges trả về Redis key cache danh sách badge đã earned
func cacheKeyEarnedBadges(userID string) string {
	return fmt.Sprintf("earned_badges:%s", userID)
}

// CheckAndAwardBadges kiểm tra và trao badge mới — tối ưu: 1 query batch + Redis cache 10 phút
func CheckAndAwardBadges(userID string) {
	cacheKey := cacheKeyEarnedBadges(userID)

	// 1. Kiểm tra cache trước — nếu đã check trong 10 phút thì bỏ qua
	if config.RedisClient != nil {
		cached, err := config.RedisClient.Exists(config.Ctx, cacheKey+"_checked").Result()
		if err == nil && cached > 0 {
			return // Vừa được kiểm tra rồi, skip
		}
	}

	// 2. Lấy TẤT CẢ badge đã earned bằng 1 query duy nhất (thay vì 7 EXISTS riêng lẻ)
	earnedSet := map[string]bool{}
	rows, err := config.DB.Query(config.Ctx,
		`SELECT badge_id FROM user_badges WHERE user_id=$1`, userID)
	if err != nil {
		log.Printf("[Badge] query earned badges error for %s: %v", userID, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var bid string
		if scanErr := rows.Scan(&bid); scanErr == nil {
			earnedSet[bid] = true
		}
	}

	// 3. Chỉ chạy CheckFn cho badge chưa earned
	var newlyEarned []string
	for _, b := range badgeDefs {
		if earnedSet[b.ID] {
			continue // Đã có rồi, skip
		}
		if b.CheckFn(userID) {
			config.DB.Exec(config.Ctx,
				`INSERT INTO user_badges (user_id, badge_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`,
				userID, b.ID)
			newlyEarned = append(newlyEarned, b.ID)
		}
	}

	// 4. Đặt cache "vừa kiểm tra" trong 10 phút để tránh re-check liên tục
	if config.RedisClient != nil {
		config.RedisClient.Set(config.Ctx, cacheKey+"_checked", "1", 10*time.Minute)
		// Nếu có badge mới → xoá cache để GetBadges refresh ngay
		if len(newlyEarned) > 0 {
			config.RedisClient.Del(config.Ctx, cacheKey)
			log.Printf("[Badge] User %s earned new badges: %v", userID, newlyEarned)
		}
	}
}

// GetStreak trả về streak học tập hiện tại của user
// GET /gamification/streak
func GetStreak(c *gin.Context) {
	userID := c.GetString("user_id")

	// Cập nhật streak
	config.DB.Exec(config.Ctx, `
		INSERT INTO user_streaks (user_id, current_streak, longest_streak, last_activity_date)
		VALUES ($1, 1, 1, CURRENT_DATE)
		ON CONFLICT (user_id) DO UPDATE
		SET current_streak = CASE
			WHEN user_streaks.last_activity_date = CURRENT_DATE - INTERVAL '1 day' THEN user_streaks.current_streak + 1
			WHEN user_streaks.last_activity_date = CURRENT_DATE THEN user_streaks.current_streak
			ELSE 1
		END,
		longest_streak = GREATEST(user_streaks.longest_streak,
			CASE WHEN user_streaks.last_activity_date = CURRENT_DATE - INTERVAL '1 day' THEN user_streaks.current_streak + 1 ELSE 1 END),
		last_activity_date = CURRENT_DATE`, userID)

	var current, longest int
	var lastDate time.Time
	if err := config.DB.QueryRow(config.Ctx,
		`SELECT current_streak, longest_streak, last_activity_date FROM user_streaks WHERE user_id=$1`,
		userID).Scan(&current, &longest, &lastDate); err != nil {
		// Streak chưa tồn tại — mặc định 0
		current, longest = 0, 0
	}

	c.JSON(200, gin.H{"success": true, "data": gin.H{
		"current_streak": current,
		"longest_streak": longest,
		"last_activity":  lastDate,
	}})
}
