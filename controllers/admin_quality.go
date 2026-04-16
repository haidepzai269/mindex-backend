package controllers

import (
	"context"
	"fmt"
	"log"
	"mindex-backend/config"
	"mindex-backend/utils"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// ─── API: Quality Stats Overview ────────────────────────────────────────────

// AdminQualityStats trả về overview: avg rating, thumbs down rate, avg latency theo model
// GET /api/admin/quality/stats?days=7
func AdminQualityStats(c *gin.Context) {
	days := 7
	if d := c.Query("days"); d != "" {
		if n, err := strconv.Atoi(d); err == nil && n > 0 && n <= 90 {
			days = n
		}
	}

	rows, err := config.DB.Query(context.Background(), `
		SELECT 
			l.model_used,
			COUNT(l.id)                                                  AS total_calls,
			ROUND(AVG(l.latency_ms)::numeric, 0)                        AS avg_latency_ms,
			ROUND(AVG(CASE WHEN r.thumbs = TRUE THEN 1.0 ELSE 0.0 END) * 100, 1) AS thumbs_up_pct,
			ROUND(AVG(CASE WHEN r.thumbs = FALSE THEN 1.0 ELSE 0.0 END) * 100, 1) AS thumbs_down_pct,
			COUNT(r.id)                                                  AS total_ratings,
			ROUND(AVG(r.rating)::numeric, 2)                            AS avg_rating
		FROM ai_response_logs l
		LEFT JOIN response_ratings r ON r.log_id = l.id
		WHERE l.created_at >= NOW() - INTERVAL '1 day' * $1
		GROUP BY l.model_used
		ORDER BY total_calls DESC`,
		days,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	type ModelStat struct {
		Model         string  `json:"model"`
		TotalCalls    int64   `json:"total_calls"`
		AvgLatencyMs  float64 `json:"avg_latency_ms"`
		ThumbsUpPct   float64 `json:"thumbs_up_pct"`
		ThumbsDownPct float64 `json:"thumbs_down_pct"`
		TotalRatings  int64   `json:"total_ratings"`
		AvgRating     float64 `json:"avg_rating"`
	}

	var stats []ModelStat
	for rows.Next() {
		var s ModelStat
		rows.Scan(&s.Model, &s.TotalCalls, &s.AvgLatencyMs, &s.ThumbsUpPct, &s.ThumbsDownPct, &s.TotalRatings, &s.AvgRating)
		stats = append(stats, s)
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "data": stats, "period_days": days})
}

// ─── API: Daily Thumbs Down Rate (cho biểu đồ) ──────────────────────────────

// AdminQualityTimeline trả về thumbs down rate theo ngày
// GET /api/admin/quality/timeline?days=14
func AdminQualityTimeline(c *gin.Context) {
	days := 14
	if d := c.Query("days"); d != "" {
		if n, err := strconv.Atoi(d); err == nil && n > 0 && n <= 90 {
			days = n
		}
	}

	rows, err := config.DB.Query(context.Background(), `
		SELECT 
			TO_CHAR(l.created_at AT TIME ZONE 'Asia/Ho_Chi_Minh', 'YYYY-MM-DD') AS day,
			COUNT(DISTINCT l.id)                                               AS total_calls,
			COUNT(r.id)                                                        AS total_ratings,
			SUM(CASE WHEN r.thumbs = FALSE THEN 1 ELSE 0 END)                  AS thumbs_down_count,
			COALESCE(
				ROUND(
					SUM(CASE WHEN r.thumbs = FALSE THEN 1.0 ELSE 0.0 END) 
					/ NULLIF(COUNT(r.id), 0) * 100, 1
				), 0
			)                                                                  AS thumbs_down_pct
		FROM ai_response_logs l
		LEFT JOIN response_ratings r ON r.log_id = l.id
		WHERE l.created_at >= NOW() - INTERVAL '1 day' * $1
		GROUP BY 1
		ORDER BY 1 ASC`,
		days,
	)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	type DayStat struct {
		Day           string  `json:"day"`
		TotalCalls    int64   `json:"total_calls"`
		TotalRatings  int64   `json:"total_ratings"`
		ThumbsDown    int64   `json:"thumbs_down_count"`
		ThumbsDownPct float64 `json:"thumbs_down_pct"`
	}

	var items []DayStat
	for rows.Next() {
		var s DayStat
		rows.Scan(&s.Day, &s.TotalCalls, &s.TotalRatings, &s.ThumbsDown, &s.ThumbsDownPct)
		items = append(items, s)
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "data": items})
}

// ─── API: Low-Rated Questions ────────────────────────────────────────────────

// AdminLowRatedQuestions trả về top câu hỏi bị đánh giá thấp nhất
// GET /api/admin/quality/low-rated?limit=20
func AdminLowRatedQuestions(c *gin.Context) {
	limit := 20
	if l := c.Query("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}

	rows, err := config.DB.Query(context.Background(), `
		SELECT 
			l.id,
			l.question,
			l.answer,
			l.model_used,
			l.latency_ms,
			l.topic_label,
			l.created_at,
			r.comment,
			r.thumbs,
			r.rating
		FROM ai_response_logs l
		JOIN response_ratings r ON r.log_id = l.id
		WHERE r.thumbs = FALSE
		ORDER BY l.created_at DESC
		LIMIT $1`,
		limit,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	type LowRated struct {
		ID         string     `json:"id"`
		Question   string     `json:"question"`
		Answer     string     `json:"answer"`
		ModelUsed  string     `json:"model_used"`
		LatencyMs  int        `json:"latency_ms"`
		TopicLabel *string    `json:"topic_label"`
		CreatedAt  time.Time  `json:"created_at"`
		Comment    *string    `json:"comment"`
		Thumbs     bool       `json:"thumbs"`
		Rating     *int       `json:"rating"`
	}

	var items []LowRated
	for rows.Next() {
		var s LowRated
		rows.Scan(&s.ID, &s.Question, &s.Answer, &s.ModelUsed, &s.LatencyMs,
			&s.TopicLabel, &s.CreatedAt, &s.Comment, &s.Thumbs, &s.Rating)
		items = append(items, s)
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "data": items})
}

// ─── Alert Check (gọi từ background worker) ─────────────────────────────────

// CheckAndSendQualityAlert kiểm tra thumbs down rate trong 1 giờ gần nhất
// Nếu rate > 30%, gửi email alert tới admin
func CheckAndSendQualityAlert() {
	type AlertResult struct {
		Model         string  `db:"model_used"`
		ThumbsDownPct float64 `db:"thumbs_down_pct"`
		TotalRatings  int64   `db:"total_ratings"`
	}

	rows, err := config.DB.Query(context.Background(), `
		SELECT 
			l.model_used,
			ROUND(
				SUM(CASE WHEN r.thumbs = FALSE THEN 1.0 ELSE 0.0 END) 
				/ NULLIF(COUNT(r.id), 0) * 100, 1
			) AS thumbs_down_pct,
			COUNT(r.id) AS total_ratings
		FROM ai_response_logs l
		JOIN response_ratings r ON r.log_id = l.id
		WHERE l.created_at >= NOW() - INTERVAL '1 hour'
		GROUP BY l.model_used
		HAVING COUNT(r.id) >= 5
		   AND SUM(CASE WHEN r.thumbs = FALSE THEN 1.0 ELSE 0.0 END) / NULLIF(COUNT(r.id), 0) > 0.30`)
	if err != nil {
		log.Printf("⚠️ [AlertCheck] Query error: %v", err)
		return
	}
	defer rows.Close()

	var badModels []AlertResult
	for rows.Next() {
		var r AlertResult
		rows.Scan(&r.Model, &r.ThumbsDownPct, &r.TotalRatings)
		badModels = append(badModels, r)
	}

	if len(badModels) == 0 {
		return
	}

	// Build email body
	body := `<div style="font-family: sans-serif; max-width:600px; margin:0 auto; padding:20px; border:1px solid #eee; border-radius:10px;">
<h2 style="color:#b829ff;">⚠️ Cảnh báo Chất lượng AI — Mindex</h2>
<p>Một hoặc nhiều model AI có tỷ lệ phản hồi kém (thumbs down > 30%) trong 1 giờ qua:</p>
<table style="width:100%; border-collapse:collapse; margin-top:16px;">
<tr style="background:#f4f4f4;"><th style="padding:10px; text-align:left;">Model</th><th style="padding:10px;">Thumbs Down %</th><th style="padding:10px;">Số rating</th></tr>`

	for _, m := range badModels {
		body += fmt.Sprintf(`<tr><td style="padding:10px; border-top:1px solid #eee;">%s</td><td style="padding:10px; text-align:center; color:#e74c3c; font-weight:bold;">%.1f%%</td><td style="padding:10px; text-align:center;">%d</td></tr>`,
			m.Model, m.ThumbsDownPct, m.TotalRatings)
	}

	body += `</table>
<p style="margin-top:20px;">Vui lòng kiểm tra trang <strong>/admin/quality</strong> để xem chi tiết.</p>
<hr style="border:0;border-top:1px solid #eee;">
<p style="font-size:12px;color:#999;">Email tự động từ Mindex Quality Monitor.</p>
</div>`

	// Lấy email admin từ DB
	adminRows, err := config.DB.Query(context.Background(),
		`SELECT email FROM users WHERE role = 'admin' LIMIT 5`)
	if err != nil {
		log.Printf("⚠️ [AlertCheck] Cannot fetch admin emails: %v", err)
		return
	}
	defer adminRows.Close()

	for adminRows.Next() {
		var email string
		adminRows.Scan(&email)
		if err := utils.SendEmail(email, "⚠️ [Mindex] Cảnh báo: Model AI đang hoạt động kém", body); err != nil {
			log.Printf("❌ [AlertCheck] Failed to send alert email to %s: %v", email, err)
		} else {
			log.Printf("📧 [AlertCheck] Sent quality alert to %s (models: %d bad)", email, len(badModels))
		}
	}
}
