package utils

import (
	"context"
	"log"
	"mindex-backend/config"
	"sort"
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
}

// HybridSearch gộp kết quả từ Vector Search và Full-Text Search (BM25)
func HybridSearch(docID string, collectionID string, query string, queryVec []float32, topK int) ([]ChunkResult, error) {
	// 1. Chạy Vector Search
	vectorResults, err := vectorSearch(docID, collectionID, queryVec, topK*2)
	if err != nil {
		log.Printf("⚠️ [HybridSearch] Vector Search failed: %v", err)
	}

	// 2. Chạy Full-Text Search (BM25)
	ftsResults, err := fullTextSearch(docID, collectionID, query, topK*2)
	if err != nil {
		log.Printf("⚠️ [HybridSearch] FTS failed: %v", err)
	}

	// 3. Kết hợp bằng Reciprocal Rank Fusion (RRF)
	return ReciprocalRankFusion(vectorResults, ftsResults, topK), nil
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

func fullTextSearch(docID string, collectionID string, userQuery string, limit int) ([]ChunkResult, error) {
	var query string
	var args []interface{}

	if collectionID != "" {
		query = `
			SELECT c.id, COALESCE(c.retrieval_content, c.content), COALESCE(c.chunk_index, 0), COALESCE(c.page_number, 0), d.title, d.id,
			       ts_rank_cd(c.content_tsv, websearch_to_tsquery('simple', $1)) AS rank
			FROM document_chunks c
			JOIN documents d ON c.document_id = d.id
			JOIN collection_documents cd ON d.id = cd.document_id
			WHERE cd.collection_id = $2 AND d.status = 'ready'
			  AND c.content_tsv @@ websearch_to_tsquery('simple', $1)
			ORDER BY rank DESC LIMIT $3`
		args = []interface{}{userQuery, collectionID, limit}
	} else {
		query = `
			SELECT c.id, COALESCE(c.retrieval_content, c.content), COALESCE(c.chunk_index, 0), COALESCE(c.page_number, 0), d.title, d.id,
			       ts_rank_cd(c.content_tsv, websearch_to_tsquery('simple', $1)) AS rank
			FROM document_chunks c
			JOIN documents d ON c.document_id = d.id
			WHERE d.id = $2 AND d.status = 'ready'
			  AND c.content_tsv @@ websearch_to_tsquery('simple', $1)
			ORDER BY rank DESC LIMIT $3`
		args = []interface{}{userQuery, docID, limit}
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
