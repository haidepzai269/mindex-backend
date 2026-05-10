package controllers

import (
	"log"
	"mindex-backend/config"
	"time"

	"github.com/gin-gonic/gin"
)

// GetAnalyticsSummary tổng hợp thống kê học tập của user
// GET /analytics/summary
func GetAnalyticsSummary(c *gin.Context) {
	userID := c.GetString("user_id")

	var totalDocs, pinnedDocs int
	if err := config.DB.QueryRow(config.Ctx,
		`SELECT COUNT(*), COALESCE(SUM(CASE WHEN pinned THEN 1 ELSE 0 END), 0)
		 FROM document_references WHERE user_id = $1`, userID).Scan(&totalDocs, &pinnedDocs); err != nil {
		log.Printf("[Analytics] docs query error for user %s: %v", userID, err)
	}

	var totalFlashcardSets, totalFlashcards, rememberedCards int
	if err := config.DB.QueryRow(config.Ctx,
		`SELECT COUNT(DISTINCT fs.id), COUNT(f.id), COALESCE(SUM(CASE WHEN f.remembered THEN 1 ELSE 0 END), 0)
		 FROM flashcard_sets fs
		 LEFT JOIN flashcards f ON f.set_id = fs.id
		 WHERE fs.user_id = $1`, userID).Scan(&totalFlashcardSets, &totalFlashcards, &rememberedCards); err != nil {
		log.Printf("[Analytics] flashcards query error for user %s: %v", userID, err)
	}

	var totalQuizAttempts int
	var avgQuizScore float64
	if err := config.DB.QueryRow(config.Ctx,
		`SELECT COUNT(*), COALESCE(AVG(score), 0)
		 FROM quiz_attempts WHERE user_id = $1`, userID).Scan(&totalQuizAttempts, &avgQuizScore); err != nil {
		log.Printf("[Analytics] quizzes query error for user %s: %v", userID, err)
	}

	var totalSessions int
	if err := config.DB.QueryRow(config.Ctx,
		`SELECT COUNT(*) FROM chat_histories WHERE user_id = $1 AND message_count > 0`, userID).Scan(&totalSessions); err != nil {
		log.Printf("[Analytics] sessions query error for user %s: %v", userID, err)
	}

	masteryPct := 0.0
	if totalFlashcards > 0 {
		masteryPct = float64(rememberedCards) / float64(totalFlashcards) * 100
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"documents":     gin.H{"total": totalDocs, "pinned": pinnedDocs},
			"flashcards":    gin.H{"sets": totalFlashcardSets, "total": totalFlashcards, "remembered": rememberedCards, "mastery_pct": masteryPct},
			"quizzes":       gin.H{"attempts": totalQuizAttempts, "avg_score": avgQuizScore},
			"chat_sessions": totalSessions,
		},
	})
}

// GetAnalyticsActivity trả về hoạt động 30 ngày gần nhất
// GET /analytics/activity
func GetAnalyticsActivity(c *gin.Context) {
	userID := c.GetString("user_id")

	rows, err := config.DB.Query(config.Ctx, `
		SELECT DATE(completed_at) as day, COUNT(*) as quiz_count, COALESCE(AVG(score),0) as avg_score
		FROM quiz_attempts
		WHERE user_id = $1 AND completed_at >= NOW() - INTERVAL '30 days'
		GROUP BY DATE(completed_at)
		ORDER BY day ASC`, userID)
	if err != nil {
		log.Printf("[Analytics] activity query error for user %s: %v", userID, err)
		c.JSON(200, gin.H{"success": true, "data": []any{}})
		return
	}
	defer rows.Close()

	type DayActivity struct {
		Day      string  `json:"day"`
		Quizzes  int     `json:"quizzes"`
		AvgScore float64 `json:"avg_score"`
	}
	var activity []DayActivity
	for rows.Next() {
		var d DayActivity
		var day time.Time
		if err := rows.Scan(&day, &d.Quizzes, &d.AvgScore); err != nil {
			log.Printf("[Analytics] row scan error: %v", err)
			continue
		}
		d.Day = day.Format("2006-01-02")
		activity = append(activity, d)
	}
	if err := rows.Err(); err != nil {
		log.Printf("[Analytics] rows iteration error for user %s: %v", userID, err)
	}
	if activity == nil {
		activity = []DayActivity{}
	}

	c.JSON(200, gin.H{"success": true, "data": activity})
}
