package utils

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

type HFClassificationRequest struct {
	Inputs     string `json:"inputs"`
	Parameters struct {
		CandidateLabels []string `json:"candidate_labels"`
	} `json:"parameters"`
}

type HFClassificationResponse struct {
	Label string  `json:"label"`
	Score float32 `json:"score"`
}

// ClassifyPersonaWithHF sử dụng mô hình facebook/bart-large-mnli để phân loại nội dung
func ClassifyPersonaWithHF(rawText string) (string, error) {
	// Lấy tối đa khoảng 1000 từ đầu tiên
	words := strings.Fields(rawText)
	wordCount := len(words)
	limit := 1000
	if wordCount < limit {
		limit = wordCount
	}
	sampleText := strings.Join(words[:limit], " ")

	log.Printf("🔍 [HF Debug] RawText Length: %d chars, WordCount: %d. Sending %d words to HF.", len(rawText), wordCount, limit)
	if len(sampleText) > 200 {
		log.Printf("🔍 [HF Debug] Sample Preview: %s...", sampleText[:200])
	} else {
		log.Printf("🔍 [HF Debug] Sample Preview: %s", sampleText)
	}

	// Sử dụng mô tả tự nhiên để Zero-Shot AI hiểu tốt hơn
	labelMap := map[string]string{
		"Information Technology, Computer Science, Engineering, Artificial Intelligence and Software": "engineer",
		"Medical documents, healthcare, clinical reports, biology and medicine":                        "doctor",
		"Legal documents, laws, regulations, contracts and judicial proceedings":                      "legal",
		"Business, economics, marketing plans, finance and corporate strategy":                         "business",
		"Academic papers, scientific research, philosophy and deep analysis":                           "researcher",
		"General education, student assignments, introductory topics or basic school work":             "student",
	}

	candidateLabels := []string{}
	for k := range labelMap {
		candidateLabels = append(candidateLabels, k)
	}

	reqBody := HFClassificationRequest{
		Inputs: sampleText,
	}
	reqBody.Parameters.CandidateLabels = candidateLabels

	jsonBytes, _ := json.Marshal(reqBody)

	// Cơ chế Pooling & Retry
	var lastErr error
	for i := 0; i < 2; i++ { 
		key, alias := HFPool.GetKey()
		if key == "" {
			return "student", fmt.Errorf("không có API Key cho HFPool")
		}

		// URL mới theo thông báo 410 Gone của Hugging Face
		apiURL := "https://router.huggingface.co/hf-inference/models/facebook/bart-large-mnli"
		req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonBytes))
		if err != nil {
			return "student", err
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")

		log.Printf("🧪 HF Classification: Đang thử với %s (Custom Labels)", alias)

		client := &http.Client{Timeout: 60 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			log.Printf("❌ [HF Error] API call timeout/error: %v", err)
			continue
		}
		defer resp.Body.Close()

		bodyBytes, _ := io.ReadAll(resp.Body)

		if resp.StatusCode != 200 {
			lastErr = fmt.Errorf("HF API failed (%s): %s", resp.Status, string(bodyBytes))
			log.Printf("❌ [HF Error] Status %d: %s", resp.StatusCode, string(bodyBytes))
			if resp.StatusCode == 429 || resp.StatusCode == 503 {
				continue
			}
			return "student", lastErr
		}

		// Cập nhật Quota Status từ Token Headers
		UpdateKeyStatusFromHeaders("huggingface", alias, key, resp.Header)

		var hfResList []HFClassificationResponse
		if err := json.Unmarshal(bodyBytes, &hfResList); err != nil {
			log.Printf("❌ [HF Error] Decode error: %v. Raw body: %s", err, string(bodyBytes))
			return "student", err
		}

		if len(hfResList) == 0 {
			log.Printf("❌ [HF Error] Empty result list from HF. Body: %s", string(bodyBytes))
			continue
		}

		// LOG TOÀN BỘ KẾT QUẢ
		log.Printf("📊 [HF Result] Labels returned: %d", len(hfResList))
		for i, item := range hfResList {
			if i < 3 { // Show top 3
				log.Printf("   - %s: %.4f", item.Label, item.Score)
			}
		}

		// Kết quả đầu tiên là kết quả có điểm cao nhất
		winnerLabel := hfResList[0].Label
		personaKey, ok := labelMap[winnerLabel]
		if !ok {
			log.Printf("⚠️ [HF Error] Winner label not found in map: %s", winnerLabel)
			return "student", nil
		}
		
		log.Printf("🎯 [HF Final] Winner: %s -> Assigned Key: %s", winnerLabel, personaKey)
		LogTokenUsage(TokenUsageLog{
			Service:     "huggingface",
			Operation:   "classify",
			TotalTokens: limit,
			KeyAlias:    alias,
			Status:      "ok",
		})
		return personaKey, nil
	}

	return "student", fmt.Errorf("tất cả API keys trong HFPool đã thất bại: %v", lastErr)
}

// RewriteQueryForSearch sử dụng LLM của Hugging Face để "mở rộng" câu truy vấn tìm kiếm
func RewriteQueryForSearch(query string) (string, error) {
	if len(query) < 15 {
		return query, nil // Chỉ tối ưu hóa với các câu truy vấn mang tính mô tả dài
	}

	// Payload cho OpenAI-compatible Chat API trên HF Router
	payload := map[string]interface{}{
		"model": "meta-llama/Llama-3.2-3B-Instruct",
		"messages": []map[string]string{
			{"role": "system", "content": "You are an expert search query optimizer for a technical document library called Mindex. \n\nYour task: Expand the user's query into a list of semantically related keywords and technical concepts. \nCRITICAL: If the query is in English, include Vietnamese equivalents. If it's in Vietnamese, include English technical terms. \nExample: 'realtime' -> 'thời gian thực, đồng bộ, websocket, sse, socket.io, real-time communication'. \n\nOutput ONLY the expanded keywords separated by spaces or commas, no conversational filler, same language as query + translation."},
			{"role": "user", "content": "Query: " + query},
		},
		"max_tokens": 100,
	}

	jsonBytes, _ := json.Marshal(payload)

	var lastErr error
	for i := 0; i < 2; i++ {
		key, alias := HFPool.GetKey()
		if key == "" {
			return query, nil // Fallback
		}

		// HF Inference OpenAI-compatible endpoint
		apiURL := "https://router.huggingface.co/v1/chat/completions"
		req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonBytes))
		if err != nil {
			return query, err
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")

		log.Printf("🤖 [HF Search] Đang tối ưu query với %s", alias)

		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			lastErr = fmt.Errorf("HF Rewrite failed (%d): %s", resp.StatusCode, string(body))
			continue
		}

		// Cập nhật Quota Status từ Token Headers
		UpdateKeyStatusFromHeaders("huggingface", alias, key, resp.Header)

		var chatRes struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&chatRes); err != nil {
			return query, err
		}

		if len(chatRes.Choices) > 0 {
			optimized := strings.TrimSpace(chatRes.Choices[0].Message.Content)
			log.Printf("💡 [HF Search] Optimized: %s -> %s", query, optimized)
			LogTokenUsage(TokenUsageLog{
				Service:     "huggingface",
				Operation:   "rewrite",
				TotalTokens: len(query) / 4,
				KeyAlias:    alias,
				Status:      "ok",
			})
			return optimized, nil
		}
	}

	log.Printf("⚠️ [HF Search] Rewrite failed, using original query. Error: %v", lastErr)
	return query, nil
}
