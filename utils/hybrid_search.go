package utils

import (
	"context"
	"log"
	"mindex-backend/config"
	"sort"
	"strings"
)

type ChunkResult struct {
	ID               string  `json:"id"`
	Content          string  `json:"content"`
	RetrievalContent string  `json:"retrieval_content"`
	ChunkIndex       int     `json:"chunk_index"`
	PageNumber       int     `json:"page"`
	DocTitle         string  `json:"doc_title"`
	DocID            string  `json:"doc_id"`
	Score            float64 `json:"score"`
	SocialBoost      float64 `json:"-"` // popularity bonus, applied after RRF
}

// HybridSearch gộp kết quả từ Vector Search và Full-Text Search (BM25).
// Trả về (results, maxVectorSim, error) trong đó maxVectorSim là cosine similarity
// cao nhất từ vector search (0–1), dùng để detect câu hỏi off-topic.
func HybridSearch(docID string, collectionID string, query string, queryVec []float32, topK int) ([]ChunkResult, float64, error) {
	// 1. Chạy Vector Search
	vectorResults, err := vectorSearch(docID, collectionID, queryVec, topK*2)
	if err != nil {
		log.Printf("⚠️ [HybridSearch] Vector Search failed: %v", err)
	}

	// Ghi lại max cosine similarity trước khi RRF thay thế Score
	var maxVectorSim float64
	if len(vectorResults) > 0 {
		maxVectorSim = vectorResults[0].Score // vectorSearch sắp xếp DESC theo similarity
	}

	// 2. Chạy Full-Text Search (BM25)
	ftsResults, err := fullTextSearch(docID, collectionID, query, topK*2)
	if err != nil {
		log.Printf("⚠️ [HybridSearch] FTS failed: %v", err)
	}

	// 3. Kết hợp bằng Reciprocal Rank Fusion (RRF)
	return ReciprocalRankFusion(vectorResults, ftsResults, topK), maxVectorSim, nil
}

// GlobalHybridSearch searches across ready, non-expired community documents in Mindex.
func GlobalHybridSearch(query string, queryVec []float32, topK int) ([]ChunkResult, error) {
	vectorResults, err := globalVectorSearch(queryVec, topK*2)
	if err != nil {
		log.Printf("[GlobalHybridSearch] Vector Search failed: %v", err)
	}

	ftsResults, err := globalFullTextSearch(query, topK*2)
	if err != nil {
		log.Printf("[GlobalHybridSearch] FTS failed: %v", err)
	}

	// RRF fusion với raw similarity — không bị nhiễm bởi social signals
	fused := ReciprocalRankFusion(vectorResults, ftsResults, topK*2)

	// Apply social signals sau RRF để không làm lệch rank ban đầu
	fused = applyGlobalSocialBoost(fused)

	return diversifyChunksByDocument(fused, topK, 3), nil
}

// applyGlobalSocialBoost cộng popularity bonus vào RRF score sau khi đã fusion.
// Social signals (upvote, query_count) chỉ là tie-breaker, không thay đổi rank cơ bản.
func applyGlobalSocialBoost(results []ChunkResult) []ChunkResult {
	if len(results) == 0 {
		return results
	}
	for i := range results {
		results[i].Score += results[i].SocialBoost
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
	return results
}

func vectorSearch(docID string, collectionID string, queryVec []float32, limit int) ([]ChunkResult, error) {
	vecStr := FloatSliceToVectorString(queryVec)
	var query string
	var args []interface{}

	if collectionID != "" {
		query = `
			SELECT c.id, COALESCE(c.retrieval_content, c.content), COALESCE(c.chunk_index, 0), COALESCE(c.page_number, 0), d.title, d.id,
			       1 - (c.embedding <=> $1::vector) AS similarity
			FROM document_chunks c
			JOIN documents d ON c.document_id = d.id
			JOIN collection_documents cd ON d.id = cd.document_id
			WHERE cd.collection_id = $2 AND d.status = 'ready'
			ORDER BY similarity DESC LIMIT $3`
		args = []interface{}{vecStr, collectionID, limit}
	} else {
		query = `
			SELECT c.id, COALESCE(c.retrieval_content, c.content), COALESCE(c.chunk_index, 0), COALESCE(c.page_number, 0), d.title, d.id,
			       1 - (c.embedding <=> $1::vector) AS similarity
			FROM document_chunks c
			JOIN documents d ON c.document_id = d.id
			WHERE d.id = $2 AND d.status = 'ready'
			ORDER BY similarity DESC LIMIT $3`
		args = []interface{}{vecStr, docID, limit}
	}

	rows, err := config.DB.Query(context.Background(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []ChunkResult
	for rows.Next() {
		var r ChunkResult
		var sim float64
		if err := rows.Scan(&r.ID, &r.RetrievalContent, &r.ChunkIndex, &r.PageNumber, &r.DocTitle, &r.DocID, &sim); err != nil {
			log.Printf("❌ [VectorSearch] Error scanning row: %v", err)
			continue
		}
		r.Score = sim
		results = append(results, r)
	}

	return results, nil
}

func globalVectorSearch(queryVec []float32, limit int) ([]ChunkResult, error) {
	vecStr := FloatSliceToVectorString(queryVec)
	// Tách raw similarity (dùng cho ORDER BY → RRF rank) và social bonus (lưu riêng → apply sau RRF)
	query := `
		SELECT c.id, COALESCE(c.retrieval_content, c.content), COALESCE(c.chunk_index, 0), COALESCE(c.page_number, 0), d.title, d.id,
		       1 - (c.embedding <=> $1::vector) AS similarity,
		       LEAST(COALESCE(d.upvote_count, 0)::float / 1000, 0.05)
		       + LEAST(COALESCE(d.query_count, 0)::float / 5000, 0.03) AS social_boost
		FROM document_chunks c
		JOIN documents d ON c.document_id = d.id
		WHERE d.status = 'ready'
		  AND COALESCE(d.is_public, FALSE) = TRUE
		  AND (d.expired_at IS NULL OR d.expired_at > NOW())
		  AND c.embedding IS NOT NULL
		ORDER BY similarity DESC LIMIT $2`

	rows, err := config.DB.Query(context.Background(), query, vecStr, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []ChunkResult
	for rows.Next() {
		var r ChunkResult
		if err := rows.Scan(&r.ID, &r.RetrievalContent, &r.ChunkIndex, &r.PageNumber, &r.DocTitle, &r.DocID, &r.Score, &r.SocialBoost); err != nil {
			log.Printf("[GlobalVectorSearch] Error scanning row: %v", err)
			continue
		}
		results = append(results, r)
	}

	return results, nil
}

func fullTextSearch(docID string, collectionID string, userQuery string, limit int) ([]ChunkResult, error) {
	var query string
	var args []interface{}
	normQuery := normalizeFTSQuery(userQuery)

	if collectionID != "" {
		query = `
			SELECT c.id, COALESCE(c.retrieval_content, c.content), COALESCE(c.chunk_index, 0), COALESCE(c.page_number, 0), d.title, d.id,
			       ts_rank_cd(c.content_tsv, websearch_to_tsquery('simple', $1)) AS rank
			FROM document_chunks c
			JOIN documents d ON c.document_id = d.id
			JOIN collection_documents cd ON d.id = cd.document_id
			WHERE cd.collection_id = $2 AND d.status = 'ready'
			  AND (c.content_tsv @@ websearch_to_tsquery('simple', $1)
			    OR c.content_tsv @@ websearch_to_tsquery('simple', $4))
			ORDER BY rank DESC LIMIT $3`
		args = []interface{}{userQuery, collectionID, limit, normQuery}
	} else {
		query = `
			SELECT c.id, COALESCE(c.retrieval_content, c.content), COALESCE(c.chunk_index, 0), COALESCE(c.page_number, 0), d.title, d.id,
			       ts_rank_cd(c.content_tsv, websearch_to_tsquery('simple', $1)) AS rank
			FROM document_chunks c
			JOIN documents d ON c.document_id = d.id
			WHERE d.id = $2 AND d.status = 'ready'
			  AND (c.content_tsv @@ websearch_to_tsquery('simple', $1)
			    OR c.content_tsv @@ websearch_to_tsquery('simple', $4))
			ORDER BY rank DESC LIMIT $3`
		args = []interface{}{userQuery, docID, limit, normQuery}
	}

	rows, err := config.DB.Query(context.Background(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []ChunkResult
	for rows.Next() {
		var r ChunkResult
		var rank float64
		if err := rows.Scan(&r.ID, &r.RetrievalContent, &r.ChunkIndex, &r.PageNumber, &r.DocTitle, &r.DocID, &rank); err != nil {
			continue
		}
		r.Score = rank
		results = append(results, r)
	}

	return results, nil
}

// globalFullTextSearch ranks ready, non-expired documents by full-text relevance.
func globalFullTextSearch(userQuery string, limit int) ([]ChunkResult, error) {
	normQuery := normalizeFTSQuery(userQuery)
	query := `
		SELECT c.id, COALESCE(c.retrieval_content, c.content), COALESCE(c.chunk_index, 0), COALESCE(c.page_number, 0), d.title, d.id,
		       ts_rank_cd(c.content_tsv, websearch_to_tsquery('simple', $1)) AS rank,
		       LEAST(COALESCE(d.upvote_count, 0)::float / 1000, 0.05)
		       + LEAST(COALESCE(d.query_count, 0)::float / 5000, 0.03) AS social_boost
		FROM document_chunks c
		JOIN documents d ON c.document_id = d.id
		WHERE d.status = 'ready'
		  AND COALESCE(d.is_public, FALSE) = TRUE
		  AND (d.expired_at IS NULL OR d.expired_at > NOW())
		  AND (c.content_tsv @@ websearch_to_tsquery('simple', $1)
		    OR c.content_tsv @@ websearch_to_tsquery('simple', $3))
		ORDER BY rank DESC LIMIT $2`

	rows, err := config.DB.Query(context.Background(), query, userQuery, limit, normQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []ChunkResult
	for rows.Next() {
		var r ChunkResult
		if err := rows.Scan(&r.ID, &r.RetrievalContent, &r.ChunkIndex, &r.PageNumber, &r.DocTitle, &r.DocID, &r.Score, &r.SocialBoost); err != nil {
			log.Printf("[GlobalFTS] Error scanning row: %v", err)
			continue
		}
		results = append(results, r)
	}

	return results, nil
}

func diversifyChunksByDocument(results []ChunkResult, topK int, maxPerDoc int) []ChunkResult {
	if topK <= 0 || len(results) == 0 {
		return nil
	}
	if maxPerDoc <= 0 {
		maxPerDoc = topK
	}

	counts := make(map[string]int)
	selected := make([]ChunkResult, 0, topK)
	skipped := make([]ChunkResult, 0)
	for _, result := range results {
		if counts[result.DocID] >= maxPerDoc {
			skipped = append(skipped, result)
			continue
		}
		selected = append(selected, result)
		counts[result.DocID]++
		if len(selected) == topK {
			return selected
		}
	}

	for _, result := range skipped {
		selected = append(selected, result)
		if len(selected) == topK {
			break
		}
	}
	return selected
}

// ReciprocalRankFusion tính điểm gộp dựa trên thứ hạng (Rank) thay vì điểm số thô (Score)
func ReciprocalRankFusion(vector, fts []ChunkResult, topK int) []ChunkResult {
	k := 60.0 // Hằng số tiêu chuẩn cho RRF
	scores := make(map[string]float64)
	chunkMap := make(map[string]ChunkResult)

	for i, r := range vector {
		scores[r.ID] += 1.0 / (k + float64(i+1))
		chunkMap[r.ID] = r
	}

	for i, r := range fts {
		scores[r.ID] += 1.0 / (k + float64(i+1))
		if _, exists := chunkMap[r.ID]; !exists {
			chunkMap[r.ID] = r
		}
	}

	// Chuyển map về slice và sắp xếp
	var final []ChunkResult
	for id, score := range scores {
		r := chunkMap[id]
		r.Score = score
		final = append(final, r)
	}

	sort.Slice(final, func(i, j int) bool {
		return final[i].Score > final[j].Score
	})

	if len(final) > topK {
		return final[:topK]
	}
	return final
}

// normalizeFTSQuery chuẩn hóa query cho FTS: bỏ dấu tiếng Việt + lowercase.
// Cần chạy migration 010_fts_unaccent.sql trước để content_tsv cũng dùng unaccent.
func normalizeFTSQuery(q string) string {
	return strings.ToLower(RemoveVietnameseSigns(strings.TrimSpace(q)))
}

// QuickBM25Check kiểm tra nhanh bằng full-text search (không cần embedding).
// Trả về true nếu có ít nhất 1 chunk khớp keyword. Dùng để pre-filter
// trước khi gọi embedding API, tiết kiệm chi phí với câu hỏi hoàn toàn off-topic.
func QuickBM25Check(docID, collectionID, query string) bool {
	results, err := fullTextSearch(docID, collectionID, query, 1)
	return err == nil && len(results) > 0
}

// HybridSearchByDocIDs gộp kết quả từ Vector Search và Full-Text Search trên danh sách DocIDs
func HybridSearchByDocIDs(docIDs []string, query string, topK int) ([]string, error) {
	// 1. Sinh embedding cho query
	queryVec, err := GenerateEmbedding(context.Background(), query)
	if err != nil {
		return nil, err
	}

	// 2. Chạy Vector Search
	vectorResults, err := vectorSearchByDocIDs(docIDs, queryVec, topK*2)
	if err != nil {
		log.Printf("⚠️ [HybridSearchByDocIDs] Vector Search failed: %v", err)
	}

	// 3. Chạy Full-Text Search
	ftsResults, err := fullTextSearchByDocIDs(docIDs, query, topK*2)
	if err != nil {
		log.Printf("⚠️ [HybridSearchByDocIDs] FTS failed: %v", err)
	}

	// 4. Fusion
	fused := ReciprocalRankFusion(vectorResults, ftsResults, topK)

	var final []string
	for _, r := range fused {
		final = append(final, r.RetrievalContent)
	}
	return final, nil
}

func vectorSearchByDocIDs(docIDs []string, queryVec []float32, limit int) ([]ChunkResult, error) {
	vecStr := FloatSliceToVectorString(queryVec)
	query := `
		SELECT c.id, COALESCE(c.retrieval_content, c.content), COALESCE(c.chunk_index, 0), COALESCE(c.page_number, 0), d.title, d.id,
		       1 - (c.embedding <=> $1::vector) AS similarity
		FROM document_chunks c
		JOIN documents d ON c.document_id = d.id
		WHERE d.id = ANY($2::uuid[]) AND d.status = 'ready'
		ORDER BY similarity DESC LIMIT $3`

	rows, err := config.DB.Query(context.Background(), query, vecStr, docIDs, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []ChunkResult
	for rows.Next() {
		var r ChunkResult
		var sim float64
		if err := rows.Scan(&r.ID, &r.RetrievalContent, &r.ChunkIndex, &r.PageNumber, &r.DocTitle, &r.DocID, &sim); err != nil {
			continue
		}
		r.Score = sim
		results = append(results, r)
	}
	return results, nil
}

func fullTextSearchByDocIDs(docIDs []string, userQuery string, limit int) ([]ChunkResult, error) {
	normQuery := normalizeFTSQuery(userQuery)
	query := `
		SELECT c.id, COALESCE(c.retrieval_content, c.content), COALESCE(c.chunk_index, 0), COALESCE(c.page_number, 0), d.title, d.id,
		       ts_rank_cd(c.content_tsv, websearch_to_tsquery('simple', $1)) AS rank
		FROM document_chunks c
		JOIN documents d ON c.document_id = d.id
		WHERE d.id = ANY($2::uuid[]) AND d.status = 'ready'
		  AND (c.content_tsv @@ websearch_to_tsquery('simple', $1)
		    OR c.content_tsv @@ websearch_to_tsquery('simple', $4))
		ORDER BY rank DESC LIMIT $3`

	rows, err := config.DB.Query(context.Background(), query, userQuery, docIDs, limit, normQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []ChunkResult
	for rows.Next() {
		var r ChunkResult
		var rank float64
		if err := rows.Scan(&r.ID, &r.RetrievalContent, &r.ChunkIndex, &r.PageNumber, &r.DocTitle, &r.DocID, &rank); err != nil {
			continue
		}
		r.Score = rank
		results = append(results, r)
	}
	return results, nil
}
