package controllers

import (
	"encoding/json"
	"fmt"
	"log"
	"mindex-backend/config"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type SearchHistoryItem struct {
	ID        string    `json:"id"`
	Query     string    `json:"query"`
	CreatedAt time.Time `json:"created_at"`
}

const (
	MaxHistoryItems = 5
	HistoryCacheTTL = 24 * time.Hour
)

// getCacheKey trả về key Redis cho search history của user
func getHistoryCacheKey(userID string) string {
	return fmt.Sprintf("user:%s:search_history", userID)
}

// GetSearchHistory lấy lịch sử tìm kiếm 
func GetSearchHistory(c *gin.Context) {
	userID := c.GetString("user_id")

	// 1. Thử lấy từ Redis
	if config.RedisClient != nil {
		cacheKey := getHistoryCacheKey(userID)
		cachedData, err := config.RedisClient.Get(config.Ctx, cacheKey).Result()
		if err == nil && cachedData != "" {
			var history []SearchHistoryItem
			if err := json.Unmarshal([]byte(cachedData), &history); err == nil {
				c.JSON(200, gin.H{"success": true, "data": history})
				return
			}
		}
	}

	// 2. Cache miss -> Lấy từ DB
	rows, err := config.DB.Query(config.Ctx, `
		SELECT id, query, created_at 
		FROM search_histories 
		WHERE user_id = $1 
		ORDER BY created_at DESC 
		LIMIT $2`, userID, MaxHistoryItems)
	
	if err != nil {
		log.Printf("Error getting search history from DB: %v", err)
		c.JSON(500, gin.H{"success": false, "message": "Lỗi lấy lịch sử tìm kiếm"})
		return
	}
	defer rows.Close()

	var history []SearchHistoryItem = []SearchHistoryItem{}
	for rows.Next() {
		var item SearchHistoryItem
		if err := rows.Scan(&item.ID, &item.Query, &item.CreatedAt); err == nil {
			history = append(history, item)
		}
	}

	// 3. Cập nhật lại Cache
	if config.RedisClient != nil {
		cacheKey := getHistoryCacheKey(userID)
		if bytes, err := json.Marshal(history); err == nil {
			config.RedisClient.Set(config.Ctx, cacheKey, string(bytes), HistoryCacheTTL)
		}
	}

	c.JSON(200, gin.H{"success": true, "data": history})
}

// AddSearchHistory thêm từ khóa tìm kiếm
func AddSearchHistory(c *gin.Context) {
	userID := c.GetString("user_id")
	var req struct {
		Query string `json:"query" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "message": "Từ khóa không hợp lệ"})
		return
	}

	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" || len(req.Query) > 100 {
		c.JSON(400, gin.H{"success": false, "message": "Từ khóa rỗng hoặc quá dài"})
		return
	}

	// 1. Lưu vào Database (Sử dụng UPSERT để cập nhật created_at nếu đã tồn tại)
	var insertedItem SearchHistoryItem
	err := config.DB.QueryRow(config.Ctx, `
		INSERT INTO search_histories (user_id, query, created_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (user_id, query) 
		DO UPDATE SET created_at = EXCLUDED.created_at
		RETURNING id, query, created_at
	`, userID, req.Query).Scan(&insertedItem.ID, &insertedItem.Query, &insertedItem.CreatedAt)

	if err != nil {
		log.Printf("Error adding search history: %v", err)
		c.JSON(500, gin.H{"success": false, "message": "Không thể lưu lịch sử tìm kiếm"})
		return
	}

	// 2. Xóa các bản ghi cũ vượt giới hạn trong DB
	_, _ = config.DB.Exec(config.Ctx, `
		DELETE FROM search_histories 
		WHERE id IN (
			SELECT id FROM search_histories 
			WHERE user_id = $1 
			ORDER BY created_at DESC 
			OFFSET $2
		)
	`, userID, MaxHistoryItems)

	// 3. Xóa Cache để lần sau load lại sẽ tự fetch bản mới nhất (có thể tối ưu thêm nhưng del cache cho an toàn & đồng bộ)
	if config.RedisClient != nil {
		cacheKey := getHistoryCacheKey(userID)
		config.RedisClient.Del(config.Ctx, cacheKey)
	}

	c.JSON(200, gin.H{"success": true, "data": insertedItem})
}

// DeleteSearchHistory xóa 1 mục lịch sử bằng ID
func DeleteSearchHistory(c *gin.Context) {
	userID := c.GetString("user_id")
	historyID := c.Param("id")

	result, err := config.DB.Exec(config.Ctx, `
		DELETE FROM search_histories 
		WHERE id = $1 AND user_id = $2
	`, historyID, userID)

	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Không thể xóa lịch sử"})
		return
	}

	if result.RowsAffected() > 0 {
		// Clear cache
		if config.RedisClient != nil {
			cacheKey := getHistoryCacheKey(userID)
			config.RedisClient.Del(config.Ctx, cacheKey)
		}
	}

	c.JSON(200, gin.H{"success": true, "message": "Đã xóa lịch sử"})
}

// ClearSearchHistory xóa tất cả lịch sử tìm kiếm của người dùng
func ClearSearchHistory(c *gin.Context) {
	userID := c.GetString("user_id")

	_, err := config.DB.Exec(config.Ctx, `
		DELETE FROM search_histories 
		WHERE user_id = $1
	`, userID)

	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Không thể xóa tất cả lịch sử"})
		return
	}

	// Clear cache
	if config.RedisClient != nil {
		cacheKey := getHistoryCacheKey(userID)
		config.RedisClient.Del(config.Ctx, cacheKey)
	}

	c.JSON(200, gin.H{"success": true, "message": "Đã xóa tất cả lịch sử"})
}
