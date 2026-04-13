package controllers

import (
	"encoding/json"
	"fmt"
	"log"
	"mindex-backend/config"
	"mindex-backend/utils"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// -------------------------------------------------------
// PATCH /community/documents/:id  — Chia sẻ / Rút khỏi Thư viện chung
// -------------------------------------------------------
func AddCommunityLibrary(c *gin.Context) {
	docID := c.Param("id")
	userID := c.GetString("user_id")

	var req struct {
		IsPublic bool `json:"is_public"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "Dữ liệu không hợp lệ"})
		return
	}

	// ── Kiểm tra quyền: Chỉ cần tài liệu tồn tại trong thư viện người dùng ──
	var hasReference bool
	err := config.DB.QueryRow(config.Ctx, `
		SELECT EXISTS(
			SELECT 1 FROM document_references
			WHERE document_id = $1 AND user_id = $2
		)`, docID, userID).Scan(&hasReference)
	if err != nil || !hasReference {
		c.JSON(403, gin.H{"error": "PERMISSION_DENIED", "message": "Tài liệu không tồn tại trong thư viện của bạn"})
		return
	}

	if req.IsPublic {
		// ── Kiểm tra Quota dựa trên contributor_id (không phải user_id gốc) ──
		var contributedCount int
		var tier string
		err := config.DB.QueryRow(config.Ctx, `
			SELECT 
				(SELECT COUNT(*) FROM documents WHERE contributor_id = $1 AND is_public = TRUE),
				COALESCE(tier, 'FREE')
			FROM users WHERE id = $1`,
			userID).Scan(&contributedCount, &tier)
		
		if err != nil {
			c.JSON(500, gin.H{"error": "Lỗi kiểm tra quota"})
			return
		}

		limit := 3
		if tier == "PRO" {
			limit = 5
		} else if tier == "ULTRA" {
			limit = 10
		}

		if contributedCount >= limit {
			c.JSON(403, gin.H{
				"error":   "SHARE_QUOTA_EXCEEDED",
				"message": fmt.Sprintf("Bạn đã đóng góp tối đa %d tài liệu vào cộng đồng", limit),
			})
			return
		}

		// ── Kích hoạt share: is_public=TRUE, expired_at=+30 ngày, shared_at=NOW(), contributor_id=người thực hiện ──
		result, err := config.DB.Exec(config.Ctx,
			`UPDATE documents
			 SET is_public = TRUE,
			     expired_at = NOW() + INTERVAL '30 days',
			     shared_at = NOW(),
			     contributor_id = $1
			 WHERE id = $2`,
			userID, docID)
		if err != nil {
			log.Printf("❌ [Community] Lỗi share doc %s: %v", docID, err)
			c.JSON(500, gin.H{"error": "Không thể chia sẻ tài liệu"})
			return
		}
		if result.RowsAffected() == 0 {
			c.JSON(404, gin.H{"error": "DOCUMENT_NOT_FOUND", "message": "Tài liệu không tồn tại"})
			return
		}

		log.Printf("✅ [Community] User %s đã chia sẻ tài liệu %s vào thư viện chung (contributor)", userID, docID)
		
		// Xóa cache profile để cập nhật quota trong Billing/UI
		if config.RedisClient != nil {
			config.RedisClient.Del(config.Ctx, fmt.Sprintf("user:profile:%s", userID))
			utils.ClearCommunityCache()
		}

		c.JSON(200, gin.H{"success": true, "message": "Đã chia sẻ vào Thư viện chung. Tài liệu sẽ tồn tại 30 ngày và được gia hạn khi có tương tác."})
	} else {
		// ── RÚT: is_public=FALSE, xóa contributor_id, cập nhật expired_at ──
		// Kiểm tra đây có phải người đã contribute không
		var currentContributorID *string
		config.DB.QueryRow(config.Ctx,
			`SELECT contributor_id FROM documents WHERE id = $1`, docID).Scan(&currentContributorID)

		// Cho phép rút nếu là contributor HOẶC là chủ sở hữu gốc
		var isDocOwner bool
		config.DB.QueryRow(config.Ctx,
			`SELECT EXISTS(SELECT 1 FROM documents WHERE id = $1 AND user_id = $2)`,
			docID, userID).Scan(&isDocOwner)

		isContributor := currentContributorID != nil && *currentContributorID == userID

		if !isDocOwner && !isContributor {
			c.JSON(403, gin.H{"error": "PERMISSION_DENIED", "message": "Chỉ người đóng góp hoặc chủ sở hữu mới có thể rút tài liệu"})
			return
		}

		// Bước 1: Tắt is_public và xóa contributor_id
		result, err := config.DB.Exec(config.Ctx,
			`UPDATE documents SET is_public = FALSE, contributor_id = NULL WHERE id = $1`,
			docID)
		if err != nil || result.RowsAffected() == 0 {
			log.Printf("❌ [Community] Lỗi rút doc %s: %v", docID, err)
			c.JSON(500, gin.H{"error": "Không thể rút tài liệu"})
			return
		}

		// Bước 2: Cập nhật expired_at — chỉ khi bước 1 thành công
		var othersPinned int
		config.DB.QueryRow(config.Ctx,
			`SELECT COUNT(*) FROM document_references WHERE document_id = $1 AND pinned = TRUE`,
			docID).Scan(&othersPinned)

		if othersPinned == 0 {
			// Không còn ai ghim -> đặt lại hạn 24h
			_, _ = config.DB.Exec(config.Ctx,
				`UPDATE documents SET expired_at = NOW() + INTERVAL '24 hours' WHERE id = $1`,
				docID)
			log.Printf("🧹 [Community] Document %s unshared, expiry reset 24h", docID)
		}

		// Xóa cache profile để cập nhật quota trong Billing/UI
		if config.RedisClient != nil {
			config.RedisClient.Del(config.Ctx, fmt.Sprintf("user:profile:%s", userID))
			utils.ClearCommunityCache()
		}

		c.JSON(200, gin.H{"success": true, "message": "Đã rút tài liệu khỏi Thư viện chung"})
	}
}


// -------------------------------------------------------
// GET /community/search — Tìm kiếm trong Thư viện chung (Hybrid AI Search)
// -------------------------------------------------------
func SearchCommunity(c *gin.Context) {
	query := strings.TrimSpace(c.Query("q"))
	subject := c.Query("subject")
	personaFilter := c.DefaultQuery("persona_filter", "true") == "true"
	userPersona := c.GetString("persona")
	_ = personaFilter
	_ = userPersona

	// ── LEVEL 2 CACHE (BẬT LẠI SAU KHI DEBUG) ──
	resultCacheKey := utils.GenerateCacheKey("results", query, subject)
	if cachedData := utils.GetCache(resultCacheKey); cachedData != "" {
		var resp gin.H
		if err := json.Unmarshal([]byte(cachedData), &resp); err == nil {
			log.Printf("🚀 [Community] Result Cache Hit | Key: %s | Query: %s", resultCacheKey, query)
			c.JSON(200, resp)
			return
		}
	} else {
		log.Printf("🔍 [Community] Cache Miss | Key: %s | Query: %s", resultCacheKey, query)
	}

	// Chuẩn hóa query (xóa dấu) để so sánh Keyword Match trong SQL tốt hơn
	normQuery := query
	if query != "" {
		normQuery = utils.RemoveVietnameseSigns(query)
	}

	// ── BƯỚC 1: XÁC ĐỊNH SEARCH METHOD ──
	var queryVectorStr string
	var searchMode = "keyword"
	var exactMatchesFound int

	// Tối ưu hóa: Tránh gọi AI lãng phí nếu query đã có sẵn kết quả chính xác trong DB
	if query != "" {
		countArgs := []interface{}{normQuery}
		countSql := `
			SELECT COUNT(*) FROM documents 
			WHERE is_public = TRUE AND status = 'ready' 
			  AND (expired_at IS NULL OR expired_at > NOW()) 
			  AND REPLACE(LOWER(title), '_', ' ') ILIKE '%' || REPLACE(LOWER($1), '_', ' ') || '%'`
		
		if subject != "" && subject != "all" {
			countSql += " AND creator_persona = $2"
			countArgs = append(countArgs, subject)
		}
		
		_ = config.DB.QueryRow(config.Ctx, countSql, countArgs...).Scan(&exactMatchesFound)
	}

	// Chỉ gọi AI (Groq + Gemini) nếu không có (0) keyword match nào
	if query != "" && exactMatchesFound < 1 {
		// ── LEVEL 1 CACHE: KIỂM TRA EMBEDDING VECTOR ──
		vectorCacheKey := utils.GenerateCacheKey("vector", query, "")
		cachedVector := utils.GetCache(vectorCacheKey)

		if cachedVector != "" {
			log.Printf("🧠 [Community] Vector Cache Hit: %s", query)
			queryVectorStr = cachedVector
			searchMode = "hybrid"
		} else {
			// 1.1 Tối ưu hóa query bằng AI Orchestrator (Tự động mở rộng từ khóa tìm kiếm)
			searchSystemPrompt := `Bạn là chuyên gia tối ưu hóa câu lệnh tìm kiếm học thuật. 
Nhiệm vụ của bạn là mở rộng từ khóa tìm kiếm của người dùng thành một danh sách các thuật ngữ chuyên môn liên quan.
CHỈ trả về các từ khóa mở rộng, phân cách bằng dấu phẩy. TUYỆT ĐỐI KHÔNG chào hỏi hay giải thích.`

			searchMessages := []utils.ChatMessage{
				{Role: "system", Content: searchSystemPrompt},
				{Role: "user", Content: fmt.Sprintf("Mở rộng từ khóa tìm kiếm sau đây cho mục đích học thuật: %s", query)},
			}
			optimizedQuery, usedProvider, err := utils.AI.ChatNonStream(utils.ServiceSearch, searchMessages)
			if err == nil {
				log.Printf("💡 [Community] Optimized Search Query (via %s): '%s' -> '%s'", usedProvider, query, optimizedQuery)
				// 1.2 Nhúng vector bằng Gemini (Matching Vector Space)
				vec, err := utils.GeminiEmbedPool.EmbedWithRetry(optimizedQuery, utils.CallGeminiAPI)
				if err == nil {
					queryVectorStr = utils.FloatSliceToVectorString(vec)
					searchMode = "hybrid"
					// Lưu vào cache vector (12 giờ)
					utils.SetCache(vectorCacheKey, queryVectorStr, 12*time.Hour)
				}
			}
		}
	} else if query != "" && exactMatchesFound >= 1 {
		log.Printf("⚡ [Community] Fast Keyword Match: Found %d exact results for '%s', skipping AI to save tokens", exactMatchesFound, query)
	}

	// ── BƯỚC 2: XÂY DỰNG SQL & THỰC THI ──
	args := []interface{}{}
	var sqlQuery string

	if searchMode == "hybrid" {
		// 1. CHẾ ĐỘ TÌM KIẾM HYBRID (AI + Keyword)
		// Logic mới: 
		// - Similarity Threshold: 0.45 (Bắt được nhiều kết quả cross-language hơn)
		// - Title Match weight: 1.0
		// - Semantic Match weight: similarity
		// - Ưu tiên các tài liệu được dùng nhiều (query_count) và được vote nhiều
		sqlQuery = `
			WITH semantic_search AS (
				SELECT document_id, MAX(1 - (embedding <=> $1::vector)) as max_similarity
				FROM document_chunks
				GROUP BY document_id
				HAVING MAX(1 - (embedding <=> $1::vector)) > 0.48
			),
			scoring AS (
				SELECT 
					d.id, d.title, d.creator_persona, d.query_count, d.upvote_count,
					d.created_at, d.expired_at, COALESCE(d.shared_at, d.created_at) AS display_date, d.user_id AS owner_id,
					COALESCE(uc.name, uo.name) AS contributor_name,
					(CASE WHEN (REPLACE(LOWER(d.title), '_', ' ') ILIKE '%' || REPLACE(LOWER($2), '_', ' ') || '%') THEN 1.0 ELSE 0 END + 
					 CASE WHEN ss.max_similarity IS NOT NULL THEN ss.max_similarity ELSE 0 END) as hybrid_score
				FROM documents d
				JOIN users uo ON d.user_id = uo.id
				LEFT JOIN users uc ON d.contributor_id = uc.id
				LEFT JOIN semantic_search ss ON d.id = ss.document_id
				WHERE d.is_public = TRUE AND d.status = 'ready' 
				  AND (d.expired_at IS NULL OR d.expired_at > NOW())
				  AND (ss.max_similarity IS NOT NULL OR REPLACE(LOWER(d.title), '_', ' ') ILIKE '%' || REPLACE(LOWER($2), '_', ' ') || '%')`
		
		args = append(args, queryVectorStr, normQuery)
		
		if subject != "" && subject != "all" {
			sqlQuery += " AND d.creator_persona = $3"
			args = append(args, subject)
		}
		
		sqlQuery += `
			)
			SELECT 
				id, title, creator_persona, query_count, upvote_count, 
				created_at, expired_at, owner_id, contributor_name,
				(SELECT COUNT(*) FROM document_chunks WHERE document_id = s.id) AS chunk_count,
				hybrid_score, display_date
			FROM scoring s
			WHERE hybrid_score > 0
			ORDER BY hybrid_score DESC, display_date DESC
			LIMIT 32`

	} else if query != "" {
		// 2. CHẾ ĐỘ TÌM KIẾM KEYWORD ONLY (FALLBACK)
		sqlQuery = `
			SELECT 
				d.id, d.title, d.creator_persona, d.query_count, d.upvote_count,
				d.created_at, d.expired_at, d.user_id AS owner_id,
				COALESCE(uc.name, uo.name) AS contributor_name,
				(SELECT COUNT(*) FROM document_chunks WHERE document_id = d.id) AS chunk_count,
				0.8 as hybrid_score, COALESCE(d.shared_at, d.created_at) AS display_date
			FROM documents d
			JOIN users uo ON d.user_id = uo.id
			LEFT JOIN users uc ON d.contributor_id = uc.id
			WHERE d.is_public = TRUE AND d.status = 'ready' 
			  AND (d.expired_at IS NULL OR d.expired_at > NOW())
			  AND REPLACE(LOWER(d.title), '_', ' ') ILIKE '%' || REPLACE(LOWER($1), '_', ' ') || '%'`
		
		args = append(args, "%"+normQuery+"%")
		
		if subject != "" && subject != "all" {
			sqlQuery += " AND d.creator_persona = $2"
			args = append(args, subject)
		}
		
		sqlQuery += " ORDER BY display_date DESC LIMIT 32"

	} else {
		// 3. CHẾ ĐỘ DUYỆT (BROWSE MODE - KHI MỚI VÀO TRANG)
		sqlQuery = `
			SELECT 
				d.id, d.title, d.creator_persona, d.query_count, d.upvote_count,
				d.created_at, d.expired_at, d.user_id AS owner_id,
				COALESCE(uc.name, uo.name) AS contributor_name,
				(SELECT COUNT(*) FROM document_chunks WHERE document_id = d.id) AS chunk_count,
				1.0 as hybrid_score, COALESCE(d.shared_at, d.created_at) AS display_date
			FROM documents d
			JOIN users uo ON d.user_id = uo.id
			LEFT JOIN users uc ON d.contributor_id = uc.id
			WHERE d.is_public = TRUE AND d.status = 'ready'
			  AND (d.expired_at IS NULL OR d.expired_at > NOW())`
		
		if subject != "" && subject != "all" {
			sqlQuery += " AND d.creator_persona = $1"
			args = append(args, subject)
		}
		
		sqlQuery += " ORDER BY display_date DESC LIMIT 32"
	}

	rows, err := config.DB.Query(config.Ctx, sqlQuery, args...)
	if err != nil {
		log.Printf("❌ [Community] Lỗi query search (Mode: %s): %v", searchMode, err)
		c.JSON(500, gin.H{"error": "Không thể tìm kiếm. " + err.Error()})
		return
	}
	defer rows.Close()

	type CommunityDoc struct {
		ID              string     `json:"id"`
		Title           string     `json:"title"`
		CreatorPersona  string     `json:"creator_persona"`
		QueryCount      int        `json:"query_count"`
		UpvoteCount     int        `json:"upvote_count"`
		CreatedAt       time.Time  `json:"created_at"`
		ExpiredAt       *time.Time `json:"expired_at"`
		ContributorName string     `json:"contributor_name"`
		ChunkCount      int        `json:"chunk_count"`
		OwnerID         string     `json:"owner_id"`
		HybridScore     float64    `json:"hybrid_score"`
		DisplayDate     time.Time  `json:"display_date"`
	}

	var results []CommunityDoc
	for rows.Next() {
		var doc CommunityDoc
		err := rows.Scan(
			&doc.ID, &doc.Title, &doc.CreatorPersona,
			&doc.QueryCount, &doc.UpvoteCount, &doc.CreatedAt,
			&doc.ExpiredAt, &doc.OwnerID, &doc.ContributorName, &doc.ChunkCount,
			&doc.HybridScore, &doc.DisplayDate,
		)
		if err != nil {
			log.Printf("⚠️ [Community] Scan error: %v", err)
			continue
		}
		results = append(results, doc)
	}

	if results == nil {
		results = []CommunityDoc{}
	}

	finalResp := gin.H{
		"success": true,
		"data": gin.H{
			"results":     results,
			"search_mode": searchMode,
			"meta":        gin.H{"total": len(results)},
		},
	}

	// ── LƯU VÀO RESULT CACHE (5 PHÚT) ──
	if respBytes, err := json.Marshal(finalResp); err == nil {
		utils.SetCache(resultCacheKey, string(respBytes), 5*time.Minute)
	}

	c.JSON(200, finalResp)
}

// -------------------------------------------------------
// GET /community/documents/:id — Chi tiết tài liệu công khai
// -------------------------------------------------------
func GetCommunityDocumentDetail(c *gin.Context) {
	docID := c.Param("id")

	type DocDetail struct {
		ID              string     `json:"id"`
		Title           string     `json:"title"`
		CreatorPersona  string     `json:"creator_persona"`
		QueryCount      int        `json:"query_count"`
		UpvoteCount     int        `json:"upvote_count"`
		CreatedAt       time.Time  `json:"created_at"`
		ExpiredAt       *time.Time `json:"expired_at"`
		ContributorName string     `json:"contributor_name"`
		ChunkCount      int        `json:"chunk_count"`
		Preview         string     `json:"preview"` // 500 từ đầu từ chunks
	}

	var doc DocDetail
	err := config.DB.QueryRow(config.Ctx, `
		SELECT 
			d.id, d.title, d.creator_persona,
			d.query_count, d.upvote_count, d.created_at, d.expired_at,
			u.name,
			(SELECT COUNT(*) FROM document_chunks WHERE document_id = d.id)
		FROM documents d
		JOIN users u ON d.user_id = u.id
		WHERE d.id = $1
		  AND (d.expired_at IS NULL OR d.expired_at > NOW())`,
		docID,
	).Scan(
		&doc.ID, &doc.Title, &doc.CreatorPersona,
		&doc.QueryCount, &doc.UpvoteCount, &doc.CreatedAt, &doc.ExpiredAt,
		&doc.ContributorName, &doc.ChunkCount,
	)
	if err != nil {
		c.JSON(404, gin.H{"success": false, "error": "DOCUMENT_NOT_FOUND", "message": "Tài liệu không tồn tại"})
		return
	}

	// Lấy preview: ghép nội dung vài chunk đầu tiên (~500 từ)
	var previewBuilder strings.Builder
	previewRows, _ := config.DB.Query(config.Ctx,
		`SELECT content FROM document_chunks WHERE document_id = $1 ORDER BY chunk_index ASC LIMIT 3`,
		docID)
	if previewRows != nil {
		defer previewRows.Close()
		for previewRows.Next() {
			var content string
			if err := previewRows.Scan(&content); err == nil {
				previewBuilder.WriteString(content)
				previewBuilder.WriteString(" ")
			}
		}
	}

	preview := previewBuilder.String()
	if len(preview) > 600 {
		preview = preview[:600] + "..."
	}
	doc.Preview = strings.TrimSpace(preview)

	c.JSON(200, gin.H{"success": true, "data": doc})
}

// -------------------------------------------------------
// POST /community/documents/:id/use — Thêm vào Thư viện cá nhân
// -------------------------------------------------------
func UseCommunityDocument(c *gin.Context) {
	docID := c.Param("id")
	userID := c.GetString("user_id")

	// Kiểm tra tài liệu tồn tại và là public
	var exists bool
	err := config.DB.QueryRow(config.Ctx,
		`SELECT EXISTS(SELECT 1 FROM documents WHERE id=$1 AND status='ready')`,
		docID).Scan(&exists)
	if err != nil || !exists {
		c.JSON(404, gin.H{"success": false, "error": "DOCUMENT_NOT_FOUND", "message": "Tài liệu không tồn tại hoặc chưa sẵn sàng"})
		return
	}

	// Kiểm tra xem đã có trong library chưa (tránh duplicate)
	var alreadyExists bool
	config.DB.QueryRow(config.Ctx,
		`SELECT EXISTS(SELECT 1 FROM document_references WHERE user_id=$1 AND document_id=$2)`,
		userID, docID).Scan(&alreadyExists)

	if alreadyExists {
		c.JSON(200, gin.H{
			"success": true,
			"data": gin.H{
				"document_id":  docID,
				"message":      "Tài liệu đã có trong thư viện của bạn",
				"is_duplicate": true,
			},
		})
		return
	}

	// Tạo bản ghi document_references (liên kết — không tốn bộ nhớ thêm)
	_, err = config.DB.Exec(config.Ctx,
		`INSERT INTO document_references (user_id, document_id, is_owner, pinned)
		 VALUES ($1, $2, FALSE, FALSE)
		 ON CONFLICT (user_id, document_id) DO NOTHING`,
		userID, docID)
	if err != nil {
		log.Printf("❌ [Community] Lỗi thêm doc_ref user=%s doc=%s: %v", userID, docID, err)
		c.JSON(500, gin.H{"success": false, "error": "Không thể thêm tài liệu vào thư viện"})
		return
	}

	// Tăng query_count và gia hạn expired_at (Survival of the Fittest)
	go RefreshPublicDocExpiry(docID)

	log.Printf("✅ [Community] User %s đã thêm tài liệu công khai %s vào thư viện", userID, docID)
	c.JSON(201, gin.H{
		"success": true,
		"data": gin.H{
			"document_id":  docID,
			"message":      "Tài liệu đã được thêm vào thư viện của bạn. Không tốn quota Embedding! 🎉",
			"is_duplicate": false,
		},
	})
}

// -------------------------------------------------------
// POST /community/documents/:id/upvote — Bình chọn tài liệu
// -------------------------------------------------------
func UpvoteCommunityDocument(c *gin.Context) {
	docID := c.Param("id")
	userID := c.GetString("user_id")

	// Dùng bảng document_votes để tránh vote lặp
	// Bảng này được tạo qua migration nếu chưa có
	_, err := config.DB.Exec(config.Ctx,
		`INSERT INTO document_votes (user_id, document_id) VALUES ($1, $2)`,
		userID, docID)
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
			c.JSON(409, gin.H{
				"success": false,
				"error":   "ALREADY_VOTED",
				"message": "Bạn đã bình chọn cho tài liệu này rồi",
			})
			return
		}
		log.Printf("❌ [Community] Lỗi upvote doc=%s user=%s: %v", docID, userID, err)
		c.JSON(500, gin.H{"success": false, "error": "Không thể bình chọn"})
		return
	}

	// Tăng upvote_count trên documents
	config.DB.Exec(config.Ctx,
		`UPDATE documents SET upvote_count = upvote_count + 1 WHERE id = $1`,
		docID)

	// Lấy upvote_count mới nhất
	var upvoteCount int
	config.DB.QueryRow(config.Ctx, `SELECT upvote_count FROM documents WHERE id = $1`, docID).Scan(&upvoteCount)

	// Tự động thưởng bonus pin slot nếu đạt 100 upvotes
	go checkAndRewardContributor(docID)

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"upvote_count": upvoteCount,
			"user_voted":   true,
		},
	})
}

// -------------------------------------------------------
// GET /community/my-contributions — Danh sách đóng góp của tôi
// -------------------------------------------------------
func GetMyContributions(c *gin.Context) {
	userID := c.GetString("user_id")

	// Lấy quota — đếm theo contributor_id (người thực sự đóng góp, không phải chủ sở hữu gốc)
	var publicCount int
	config.DB.QueryRow(config.Ctx,
		`SELECT COUNT(*) FROM documents WHERE contributor_id = $1 AND is_public = TRUE`,
		userID).Scan(&publicCount)

	// Lấy danh sách tài liệu tôi đã đóng góp (dù là của tài liệu của mình hay được share)
	rows, err := config.DB.Query(config.Ctx, `
		SELECT 
			d.id, d.title, d.query_count, d.upvote_count,
			d.expired_at, d.created_at
		FROM documents d
		WHERE d.contributor_id = $1 AND d.is_public = TRUE
		ORDER BY d.query_count DESC, d.created_at DESC`,
		userID)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": "Không thể tải danh sách"})
		return
	}
	defer rows.Close()

	type Contribution struct {
		DocumentID    string     `json:"document_id"`
		Title         string     `json:"title"`
		QueryCount    int        `json:"query_count"`
		UpvoteCount   int        `json:"upvote_count"`
		ExpiredAt     *time.Time `json:"expired_at"`
		CreatedAt     time.Time  `json:"created_at"`
		DaysRemaining *int       `json:"days_remaining"`
	}

	var contributions []Contribution
	for rows.Next() {
		var c Contribution
		if err := rows.Scan(&c.DocumentID, &c.Title, &c.QueryCount, &c.UpvoteCount, &c.ExpiredAt, &c.CreatedAt); err != nil {
			continue
		}
		if c.ExpiredAt != nil {
			remaining := int(time.Until(*c.ExpiredAt).Hours() / 24)
			c.DaysRemaining = &remaining
		}
		contributions = append(contributions, c)
	}

	if contributions == nil {
		contributions = []Contribution{}
	}

	var tier string
	_ = config.DB.QueryRow(config.Ctx, `SELECT COALESCE(tier, 'FREE') FROM users WHERE id = $1`, userID).Scan(&tier)
	limit := 3
	if tier == "PRO" {
		limit = 5
	} else if tier == "ULTRA" {
		limit = 10
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"quota":         gin.H{"used": publicCount, "limit": limit},
			"contributions": contributions,
		},
	})
}


// RefreshPublicDocExpiry: Gia hạn expired_at khi có tương tác (Survival of the Fittest)
func RefreshPublicDocExpiry(docID string) {
	_, err := config.DB.Exec(config.Ctx, `
		UPDATE documents 
		SET expired_at = NOW() + INTERVAL '30 days',
		    query_count = query_count + 1
		WHERE id = $1
		  AND is_public = TRUE
		  AND (expired_at IS NULL OR expired_at > NOW())`,
		docID)
	if err != nil {
		log.Printf("⚠️ [SotF] Không thể gia hạn doc %s: %v", docID, err)
	}
}

// -------------------------------------------------------
// Helper: Kiểm tra và thưởng pin slot cho contributor
// -------------------------------------------------------
func checkAndRewardContributor(docID string) {
	var upvoteCount int
	var contributorRewarded bool
	var ownerID string

	err := config.DB.QueryRow(config.Ctx, `
		SELECT d.upvote_count, d.contributor_rewarded, d.user_id
		FROM documents d WHERE d.id = $1`, docID).Scan(&upvoteCount, &contributorRewarded, &ownerID)
	if err != nil || contributorRewarded || upvoteCount < 100 {
		return
	}

	// Đánh dấu đã thưởng
	config.DB.Exec(config.Ctx, `UPDATE documents SET contributor_rewarded=TRUE WHERE id=$1`, docID)
	log.Printf("🏆 [Gamification] Tài liệu %s đạt 100 upvote! Đã thưởng pin slot cho user %s", docID, ownerID)
}

// itoa là helper nhỏ để xây dựng dynamic SQL args ($1, $2, ...)
func itoa(i int) string {
	return fmt.Sprintf("%d", i)
}
