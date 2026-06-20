package controllers

import (
	"encoding/json"
	"fmt"
	"log"
	"mindex-backend/config"
	"mindex-backend/utils"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

var validPeriods = map[string]bool{
	"7d": true, "30d": true, "6m": true, "1y": true,
}

func parsePeriod(period string) (time.Time, string, error) {
	if !validPeriods[period] {
		return time.Time{}, "", fmt.Errorf("invalid period")
	}
	now := time.Now()
	switch period {
	case "7d":
		return now.AddDate(0, 0, -7), "day", nil
	case "30d":
		return now.AddDate(0, 0, -30), "day", nil
	case "6m":
		return now.AddDate(0, -6, 0), "week", nil
	case "1y":
		return now.AddDate(-1, 0, 0), "month", nil
	}
	return time.Time{}, "", fmt.Errorf("invalid period")
}

func GetDashboardMetrics(c *gin.Context) {
	userID := c.GetString("user_id")
	period := c.Query("period")
	ctx := c.Request.Context()

	startTime, _, err := parsePeriod(period)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "VALIDATION_ERROR", "message": "Period phải là 7d, 30d, 6m hoặc 1y"})
		return
	}

	cacheKey := fmt.Sprintf("dashboard:metrics:%s:%s", userID, period)
	if cached := utils.GetCache(cacheKey); cached != "" {
		var data map[string]any
		if json.Unmarshal([]byte(cached), &data) == nil {
			c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
			return
		}
	}

	var docCount int
	if err := config.DB.QueryRow(ctx, `
		SELECT COUNT(*) FROM document_references dr
		JOIN documents d ON d.id = dr.document_id
		WHERE dr.user_id = $1
		  AND d.created_at >= $2
		  AND (d.expired_at IS NULL OR d.expired_at > NOW())`,
		userID, startTime).Scan(&docCount); err != nil {
		log.Printf("[Dashboard] metrics doc count error: %v", err)
	}

	var chatCount, messageCount int
	if err := config.DB.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(SUM(message_count), 0)
		FROM chat_histories
		WHERE user_id = $1 AND started_at >= $2 AND message_count > 0`,
		userID, startTime).Scan(&chatCount, &messageCount); err != nil {
		log.Printf("[Dashboard] metrics chat count error: %v", err)
	}

	var positiveFeedback, negativeFeedback int
	if err := config.DB.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN rr.thumbs = true THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN rr.thumbs = false THEN 1 ELSE 0 END), 0)
		FROM response_ratings rr
		WHERE rr.user_id = $1 AND rr.created_at >= $2`,
		userID, startTime).Scan(&positiveFeedback, &negativeFeedback); err != nil {
		log.Printf("[Dashboard] metrics feedback error: %v", err)
	}

	data := gin.H{
		"document_count":    docCount,
		"chat_count":        chatCount,
		"message_count":     messageCount,
		"positive_feedback": positiveFeedback,
		"negative_feedback": negativeFeedback,
		"period":            period,
	}

	if jsonData, err := json.Marshal(data); err == nil {
		utils.SetCache(cacheKey, string(jsonData), 10*time.Minute)
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}

func GetDashboardTrends(c *gin.Context) {
	userID := c.GetString("user_id")
	period := c.Query("period")
	ctx := c.Request.Context()

	startTime, groupBy, err := parsePeriod(period)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "VALIDATION_ERROR", "message": "Period phải là 7d, 30d, 6m hoặc 1y"})
		return
	}

	cacheKey := fmt.Sprintf("dashboard:trends:%s:%s", userID, period)
	if cached := utils.GetCache(cacheKey); cached != "" {
		var data map[string]any
		if json.Unmarshal([]byte(cached), &data) == nil {
			c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
			return
		}
	}

	var query string
	switch groupBy {
	case "day":
		query = `
		WITH chat_trend AS (
			SELECT date_trunc('day', started_at) AS bucket, COUNT(*) AS chats
			FROM chat_histories
			WHERE user_id = $1 AND started_at >= $2 AND message_count > 0
			GROUP BY bucket
		),
		doc_trend AS (
			SELECT date_trunc('day', d.created_at) AS bucket, COUNT(*) AS documents
			FROM document_references dr
			JOIN documents d ON d.id = dr.document_id
			WHERE dr.user_id = $1 AND d.created_at >= $2
			  AND (d.expired_at IS NULL OR d.expired_at > NOW())
			GROUP BY bucket
		)
		SELECT COALESCE(ct.bucket, dt.bucket) AS bucket,
		       COALESCE(ct.chats, 0) AS chats,
		       COALESCE(dt.documents, 0) AS documents
		FROM chat_trend ct
		FULL OUTER JOIN doc_trend dt ON ct.bucket = dt.bucket
		ORDER BY bucket ASC`
	case "week":
		query = `
		WITH chat_trend AS (
			SELECT date_trunc('week', started_at) AS bucket, COUNT(*) AS chats
			FROM chat_histories
			WHERE user_id = $1 AND started_at >= $2 AND message_count > 0
			GROUP BY bucket
		),
		doc_trend AS (
			SELECT date_trunc('week', d.created_at) AS bucket, COUNT(*) AS documents
			FROM document_references dr
			JOIN documents d ON d.id = dr.document_id
			WHERE dr.user_id = $1 AND d.created_at >= $2
			  AND (d.expired_at IS NULL OR d.expired_at > NOW())
			GROUP BY bucket
		)
		SELECT COALESCE(ct.bucket, dt.bucket) AS bucket,
		       COALESCE(ct.chats, 0) AS chats,
		       COALESCE(dt.documents, 0) AS documents
		FROM chat_trend ct
		FULL OUTER JOIN doc_trend dt ON ct.bucket = dt.bucket
		ORDER BY bucket ASC`
	default:
		query = `
		WITH chat_trend AS (
			SELECT date_trunc('month', started_at) AS bucket, COUNT(*) AS chats
			FROM chat_histories
			WHERE user_id = $1 AND started_at >= $2 AND message_count > 0
			GROUP BY bucket
		),
		doc_trend AS (
			SELECT date_trunc('month', d.created_at) AS bucket, COUNT(*) AS documents
			FROM document_references dr
			JOIN documents d ON d.id = dr.document_id
			WHERE dr.user_id = $1 AND d.created_at >= $2
			  AND (d.expired_at IS NULL OR d.expired_at > NOW())
			GROUP BY bucket
		)
		SELECT COALESCE(ct.bucket, dt.bucket) AS bucket,
		       COALESCE(ct.chats, 0) AS chats,
		       COALESCE(dt.documents, 0) AS documents
		FROM chat_trend ct
		FULL OUTER JOIN doc_trend dt ON ct.bucket = dt.bucket
		ORDER BY bucket ASC`
	}

	rows, err := config.DB.Query(ctx, query, userID, startTime)
	if err != nil {
		log.Printf("[Dashboard] trends query error for user %s: %v", userID, err)
		c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"points": []any{}, "group_by": groupBy, "period": period}})
		return
	}
	defer rows.Close()

	type TrendPoint struct {
		Date      string `json:"date"`
		Chats     int    `json:"chats"`
		Documents int    `json:"documents"`
	}

	var points []TrendPoint
	for rows.Next() {
		var p TrendPoint
		var bucket time.Time
		if err := rows.Scan(&bucket, &p.Chats, &p.Documents); err != nil {
			log.Printf("[Dashboard] trend row scan error: %v", err)
			continue
		}
		switch groupBy {
		case "month":
			p.Date = bucket.Format("2006-01")
		default:
			p.Date = bucket.Format("2006-01-02")
		}
		points = append(points, p)
	}
	if err := rows.Err(); err != nil {
		log.Printf("[Dashboard] trends rows iteration error: %v", err)
	}
	if points == nil {
		points = []TrendPoint{}
	}

	data := gin.H{
		"points":   points,
		"group_by": groupBy,
		"period":   period,
	}

	if jsonData, err := json.Marshal(data); err == nil {
		utils.SetCache(cacheKey, string(jsonData), 10*time.Minute)
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}

func GetDashboardHighlights(c *gin.Context) {
	userID := c.GetString("user_id")
	period := c.Query("period")
	ctx := c.Request.Context()

	startTime, _, err := parsePeriod(period)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "VALIDATION_ERROR", "message": "Period phải là 7d, 30d, 6m hoặc 1y"})
		return
	}

	cacheKey := fmt.Sprintf("dashboard:highlights:%s:%s", userID, period)
	if cached := utils.GetCache(cacheKey); cached != "" {
		var data map[string]any
		if json.Unmarshal([]byte(cached), &data) == nil {
			c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
			return
		}
	}

	type TopDoc struct {
		ID         string    `json:"id"`
		Title      string    `json:"title"`
		QueryCount int       `json:"query_count"`
		CreatedAt  time.Time `json:"created_at"`
	}

	docRows, err := config.DB.Query(ctx, `
		SELECT d.id, d.title, d.query_count, d.created_at
		FROM documents d
		JOIN document_references dr ON d.id = dr.document_id
		WHERE dr.user_id = $1
		  AND d.created_at >= $2
		  AND (d.expired_at IS NULL OR d.expired_at > NOW())
		ORDER BY d.query_count DESC
		LIMIT 5`, userID, startTime)
	if err != nil {
		log.Printf("[Dashboard] top docs query error: %v", err)
	}

	var topDocs []TopDoc
	if docRows != nil {
		defer docRows.Close()
		for docRows.Next() {
			var d TopDoc
			if err := docRows.Scan(&d.ID, &d.Title, &d.QueryCount, &d.CreatedAt); err != nil {
				continue
			}
			topDocs = append(topDocs, d)
		}
		if err := docRows.Err(); err != nil {
			log.Printf("[Dashboard] top docs rows error: %v", err)
		}
	}
	if topDocs == nil {
		topDocs = []TopDoc{}
	}

	type TopChat struct {
		ID            string    `json:"id"`
		Title         *string   `json:"title"`
		MessageCount  int       `json:"message_count"`
		DocumentTitle *string   `json:"document_title"`
		StartedAt     time.Time `json:"started_at"`
	}

	chatRows, err := config.DB.Query(ctx, `
		SELECT ch.id, ch.title, ch.message_count, d.title, ch.started_at
		FROM chat_histories ch
		LEFT JOIN documents d ON d.id = ch.document_id
		WHERE ch.user_id = $1
		  AND ch.started_at >= $2
		  AND ch.message_count > 0
		ORDER BY ch.message_count DESC
		LIMIT 5`, userID, startTime)
	if err != nil {
		log.Printf("[Dashboard] top chats query error: %v", err)
	}

	var topChats []TopChat
	if chatRows != nil {
		defer chatRows.Close()
		for chatRows.Next() {
			var ch TopChat
			if err := chatRows.Scan(&ch.ID, &ch.Title, &ch.MessageCount, &ch.DocumentTitle, &ch.StartedAt); err != nil {
				continue
			}
			topChats = append(topChats, ch)
		}
		if err := chatRows.Err(); err != nil {
			log.Printf("[Dashboard] top chats rows error: %v", err)
		}
	}
	if topChats == nil {
		topChats = []TopChat{}
	}

	data := gin.H{
		"top_documents": topDocs,
		"top_chats":     topChats,
		"period":        period,
	}

	if jsonData, err := json.Marshal(data); err == nil {
		utils.SetCache(cacheKey, string(jsonData), 10*time.Minute)
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}
