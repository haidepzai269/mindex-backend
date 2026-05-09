package controllers

import (
	"fmt"
	"log"
	"mindex-backend/config"

	"github.com/gin-gonic/gin"
)

// GetPublicProfile — GET /users/:id/profile (public)
func GetPublicProfile(c *gin.Context) {
	targetID := c.Param("id")

	var name, bio, avatarURL, tier string
	err := config.DB.QueryRow(config.Ctx,
		`SELECT name, COALESCE(bio,''), COALESCE(avatar_url,''), COALESCE(tier,'FREE')
		 FROM users WHERE id = $1`, targetID).Scan(&name, &bio, &avatarURL, &tier)
	if err != nil {
		c.JSON(404, gin.H{"success": false, "message": "Người dùng không tồn tại"})
		return
	}

	// Tài liệu public
	type DocItem struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		Persona     string `json:"persona"`
		UpvoteCount int    `json:"upvote_count"`
		QueryCount  int    `json:"query_count"`
	}
	var publicDocs []DocItem
	rows, err := config.DB.Query(config.Ctx, `
		SELECT d.id, d.title, d.creator_persona, d.upvote_count, d.query_count
		FROM documents d
		WHERE d.user_id = $1 AND d.is_public = TRUE AND d.status = 'ready'
		ORDER BY d.created_at DESC LIMIT 10`, targetID)
	if err != nil {
		log.Printf("[Profile] docs query error for user %s: %v", targetID, err)
	} else {
		defer rows.Close()
		for rows.Next() {
			var d DocItem
			if scanErr := rows.Scan(&d.ID, &d.Title, &d.Persona, &d.UpvoteCount, &d.QueryCount); scanErr != nil {
				log.Printf("[Profile] doc scan error: %v", scanErr)
				continue
			}
			publicDocs = append(publicDocs, d)
		}
		if rowErr := rows.Err(); rowErr != nil {
			log.Printf("[Profile] docs rows error: %v", rowErr)
		}
	}
	if publicDocs == nil {
		publicDocs = []DocItem{}
	}

	// Badges
	var badges []string
	badgeRows, err := config.DB.Query(config.Ctx,
		`SELECT badge_id FROM user_badges WHERE user_id=$1 ORDER BY earned_at ASC`, targetID)
	if err != nil {
		log.Printf("[Profile] badges query error for user %s: %v", targetID, err)
	} else {
		defer badgeRows.Close()
		for badgeRows.Next() {
			var bid string
			if scanErr := badgeRows.Scan(&bid); scanErr != nil {
				log.Printf("[Profile] badge scan error: %v", scanErr)
				continue
			}
			badges = append(badges, bid)
		}
	}
	if badges == nil {
		badges = []string{}
	}

	c.JSON(200, gin.H{"success": true, "data": gin.H{
		"id":          targetID,
		"name":        name,
		"bio":         bio,
		"avatar_url":  avatarURL,
		"tier":        tier,
		"public_docs": publicDocs,
		"badges":      badges,
	}})
}

// AdminListUsers — GET /admin/users — danh sách users với filter
func AdminListUsers(c *gin.Context) {
	search := c.Query("q")
	tier := c.Query("tier")
	page := 1

	sqlQ := `SELECT id, name, email, COALESCE(tier,'FREE'), COALESCE(avatar_url,''), created_at FROM users WHERE 1=1`
	args := []any{}
	argIdx := 1

	if search != "" {
		sqlQ += fmt.Sprintf(" AND (name ILIKE $%d OR email ILIKE $%d)", argIdx, argIdx)
		args = append(args, "%"+search+"%")
		argIdx++
	}
	if tier != "" {
		sqlQ += fmt.Sprintf(" AND tier = $%d", argIdx)
		args = append(args, tier)
		argIdx++
	}
	sqlQ += fmt.Sprintf(" ORDER BY created_at DESC LIMIT 50 OFFSET $%d", argIdx)
	args = append(args, (page-1)*50)

	rows, err := config.DB.Query(config.Ctx, sqlQ, args...)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Query error"})
		return
	}
	defer rows.Close()

	type UserRow struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Email     string `json:"email"`
		Tier      string `json:"tier"`
		AvatarURL string `json:"avatar_url"`
		CreatedAt string `json:"created_at"`
	}
	var users []UserRow
	for rows.Next() {
		var u UserRow
		var created any
		if scanErr := rows.Scan(&u.ID, &u.Name, &u.Email, &u.Tier, &u.AvatarURL, &created); scanErr != nil {
			log.Printf("[AdminUsers] row scan error: %v", scanErr)
			continue
		}
		if t, ok := created.(interface{ String() string }); ok {
			u.CreatedAt = t.String()
		}
		users = append(users, u)
	}
	if rowErr := rows.Err(); rowErr != nil {
		log.Printf("[AdminUsers] rows iteration error: %v", rowErr)
	}
	if users == nil {
		users = []UserRow{}
	}
	c.JSON(200, gin.H{"success": true, "data": users})
}

// AdminUpdateUserTier — PATCH /admin/users/:id/tier
func AdminUpdateUserTier(c *gin.Context) {
	targetID := c.Param("id")
	var req struct {
		Tier string `json:"tier" binding:"required"` // FREE | PRO | ULTRA
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "message": "Thiếu tier"})
		return
	}
	if req.Tier != "FREE" && req.Tier != "PRO" && req.Tier != "ULTRA" {
		c.JSON(400, gin.H{"success": false, "message": "Tier không hợp lệ"})
		return
	}
	config.DB.Exec(config.Ctx, `UPDATE users SET tier=$1 WHERE id=$2`, req.Tier, targetID)
	if config.RedisClient != nil {
		config.RedisClient.Del(config.Ctx, "user:profile:"+targetID)
	}
	c.JSON(200, gin.H{"success": true, "message": "Đã cập nhật tier"})
}
