package controllers

import (
	"encoding/json"
	"fmt"
	"log"
	"mindex-backend/config"
	"mindex-backend/utils"
	"mindex-backend/utils/quota"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// AdminSystemHealth trả về snapshot sức khỏe hệ thống
func AdminSystemHealth(c *gin.Context) {
	var health struct {
		TotalUsers          int    `json:"total_users"`
		NewUsers24h         int    `json:"new_users_24h"`
		NewUsers7d          int    `json:"new_users_7d"`
		TotalDocs           int    `json:"total_docs"`
		DocsProcessing      int    `json:"docs_processing"`
		DocsError           int    `json:"docs_error"`
		DocsPublic          int    `json:"docs_public"`
		DocsExpiring24h     int    `json:"docs_expiring_24h"`
		TotalChunks         int    `json:"total_chunks"`
		DbSizeBytes         int64  `json:"db_size_bytes"`
		DbSizeHuman         string `json:"db_size_human"`
		ChunksTableHuman    string `json:"chunks_table_human"`
		TokenLogsHuman      string `json:"token_logs_human"`
		ChatHistoriesHuman  string `json:"chat_histories_human"`
		WillSweepTonight    int    `json:"will_sweep_tonight"`
		ChunksToFreeTonight int    `json:"chunks_to_free_tonight"`
	}

	err := config.DB.QueryRow(config.Ctx, `
		SELECT 
			total_users, new_users_24h, new_users_7d,
			total_docs, docs_processing, docs_error, docs_public, docs_expiring_24h,
			total_chunks, db_size_bytes, db_size_human,
			chunks_table_human, token_logs_human, chat_histories_human,
			will_sweep_tonight, chunks_to_free_tonight
		FROM admin_system_health`).Scan(
		&health.TotalUsers, &health.NewUsers24h, &health.NewUsers7d,
		&health.TotalDocs, &health.DocsProcessing, &health.DocsError, &health.DocsPublic, &health.DocsExpiring24h,
		&health.TotalChunks, &health.DbSizeBytes, &health.DbSizeHuman,
		&health.ChunksTableHuman, &health.TokenLogsHuman, &health.ChatHistoriesHuman,
		&health.WillSweepTonight, &health.ChunksToFreeTonight,
	)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch health stats: " + err.Error()})
		return
	}

	// Redis stats
	redisStats := gin.H{
		"connected": config.RedisClient != nil,
	}
	if config.RedisClient != nil {
		redisStats["active_keys"], _ = config.RedisClient.DBSize(config.Ctx).Result()
		redisStats["upload_queue_len"], _ = config.RedisClient.LLen(config.Ctx, "upload_queue").Result()
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"db":         health,
			"redis":      redisStats,
			"checked_at": time.Now(),
		},
	})
}

// AdminTokenOverview trả về tổng quan sử dụng token
func AdminTokenOverview(c *gin.Context) {
	period := c.DefaultQuery("period", "24h")
	service := c.DefaultQuery("service", "all")

	intervalMap := map[string]string{
		"24h": "24 hours",
		"7d":  "7 days",
		"30d": "30 days",
	}
	interval, ok := intervalMap[period]
	if !ok {
		interval = "24 hours"
	}

	query := `
		SELECT service, operation,
			COUNT(*) AS request_count,
			SUM(total_tokens) AS total_tokens_sum,
			AVG(latency_ms)::INTEGER AS avg_latency_ms,
			COUNT(*) FILTER (WHERE status != 'ok') AS error_count
		FROM token_usage_logs
		WHERE created_at >= NOW() - $1::INTERVAL`

	args := []interface{}{interval}
	if service != "all" {
		query += " AND service = $2"
		args = append(args, service)
	}
	query += " GROUP BY service, operation ORDER BY total_tokens_sum DESC"

	rows, err := config.DB.Query(config.Ctx, query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch token logs: " + err.Error()})
		return
	}
	defer rows.Close()

	var breakdown []gin.H
	for rows.Next() {
		var s, o string
		var count, tokens, latency, errors int
		rows.Scan(&s, &o, &count, &tokens, &latency, &errors)
		breakdown = append(breakdown, gin.H{
			"service":      s,
			"operation":    o,
			"requests":     count,
			"tokens":       tokens,
			"avg_latency":  latency,
			"error_count":  errors,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"period":    period,
			"breakdown": breakdown,
		},
	})
}

// AdminListChats trả về danh sách hội thoại để audit
func AdminListChats(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	perPage, _ := strconv.Atoi(c.DefaultQuery("per_page", "30"))
	flagged := c.Query("flagged")

	query := `
		SELECT 
			ch.id, ch.session_id, u.email as user_email, d.title as doc_title,
			ch.message_count, ch.summary, ch.flagged, ch.flag_reason, ch.started_at
		FROM chat_histories ch
		JOIN users u ON u.id = ch.user_id
		LEFT JOIN documents d ON d.id = ch.document_id
		WHERE 1=1`

	args := []interface{}{}
	if flagged == "true" {
		query += " AND ch.flagged = TRUE"
	}

	query += fmt.Sprintf(" ORDER BY ch.started_at DESC LIMIT %d OFFSET %d", perPage, (page-1)*perPage)

	rows, err := config.DB.Query(config.Ctx, query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list chats: " + err.Error()})
		return
	}
	defer rows.Close()

	var chats []gin.H
	for rows.Next() {
		var id, sid, email, summary string
		var docTitle *string
		var count int
		var flag bool
		var flagReason *string
		var startedAt time.Time
		rows.Scan(&id, &sid, &email, &docTitle, &count, &summary, &flag, &flagReason, &startedAt)

		dt := ""
		if docTitle != nil {
			dt = *docTitle
		}
		fr := ""
		if flagReason != nil {
			fr = *flagReason
		}

		chats = append(chats, gin.H{
			"id":            id,
			"session_id":    sid,
			"user_email":    email,
			"doc_title":     dt,
			"message_count": count,
			"summary":       summary,
			"flagged":       flag,
			"flag_reason":   fr,
			"started_at":    startedAt,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"chats": chats,
			"page":  page,
		},
	})
}

// AdminGetChatDetail trả về chi tiết 1 session chat
func AdminGetChatDetail(c *gin.Context) {
	sessionID := c.Param("session_id")

	var chat struct {
		ID            string          `json:"id"`
		Email         string          `json:"user_email"`
		Summary       string          `json:"summary"`
		FullMessages  json.RawMessage `json:"messages"`
		Flagged       bool            `json:"flagged"`
		FlagReason    *string         `json:"flag_reason"`
		StartedAt     time.Time       `json:"started_at"`
	}

	err := config.DB.QueryRow(config.Ctx, `
		SELECT ch.id, u.email, ch.summary, ch.full_messages, ch.flagged, ch.flag_reason, ch.started_at
		FROM chat_histories ch
		JOIN users u ON u.id = ch.user_id
		WHERE ch.session_id = $1`, sessionID).Scan(
		&chat.ID, &chat.Email, &chat.Summary, &chat.FullMessages, &chat.Flagged, &chat.FlagReason, &chat.StartedAt,
	)

	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Chat session not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    chat,
	})
}

// AdminFlagChat đánh dấu session nghi vấn
func AdminFlagChat(c *gin.Context) {
	sessionID := c.Param("session_id")
	var req struct {
		Flag   bool   `json:"flag"`
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid body"})
		return
	}

	_, err := config.DB.Exec(config.Ctx, `
		UPDATE chat_histories 
		SET flagged = $1, flag_reason = $2, flagged_at = CASE WHEN $1 THEN NOW() ELSE NULL END
		WHERE session_id = $3`, req.Flag, req.Reason, sessionID)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update flag: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "session_id": sessionID, "flagged": req.Flag})
}

// AdminKeyStatus trả về trạng thái quota của các API Key từ Redis
func AdminKeyStatus(c *gin.Context) {
	if config.RedisClient == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Redis not connected"})
		return
	}

	// Lấy danh sách các key đang hoạt động
	activeKeys, err := config.RedisClient.SMembers(config.Ctx, "active_api_keys").Result()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch active keys"})
		return
	}

	results := []utils.KeyStatus{}
	for _, k := range activeKeys {
		data, err := config.RedisClient.Get(config.Ctx, k).Result()
		if err == nil {
			var ks utils.KeyStatus
			if err := json.Unmarshal([]byte(data), &ks); err == nil {
				results = append(results, ks)
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    results,
	})
}

// AdminKeyStream khởi tạo luồng SSE để update quota real-time
func AdminKeyStream(c *gin.Context) {
	if config.RedisClient == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Redis not connected"})
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("Transfer-Encoding", "chunked")

	// Subscribe channel
	pubsub := config.RedisClient.Subscribe(config.Ctx, "api_key_updates")
	defer pubsub.Close()

	ch := pubsub.Channel()

	clientGone := c.Writer.CloseNotify()
	
	log.Printf("📡 [SSE] Admin connected to KeyStream")

	for {
		select {
		case msg := <-ch:
			fmt.Fprintf(c.Writer, "event: update\ndata: %s\n\n", msg.Payload)
			c.Writer.Flush()
		case <-clientGone:
			log.Printf("🔌 [SSE] Admin disconnected from KeyStream")
			return
		}
	}
}

// AdminQuotaSummary trả về snapshot từ Quota Tracker đa tầng
func AdminQuotaSummary(c *gin.Context) {
	if quota.GlobalTracker == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Quota Tracker not initialized"})
		return
	}

	summaries := quota.GlobalTracker.GetProviderSummary()
	
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"updated_at": time.Now(),
			"providers":  summaries,
		},
	})
}
