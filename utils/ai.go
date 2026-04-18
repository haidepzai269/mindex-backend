package utils

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Gọi API nhúng vector của Gemini Google
func CallGeminiAPI(apiKey string, alias string, text string) ([]float32, error) {
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/gemini-embedding-001:embedContent?key=%s", apiKey)

	reqBody := map[string]interface{}{
		"model": "models/gemini-embedding-001",
		"content": map[string]interface{}{
			"parts": []map[string]string{{"text": text}},
		},
		"output_dimensionality": 768,
	}

	jsonBody, _ := json.Marshal(reqBody)
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Cập nhật Quota Status
	UpdateKeyStatusFromHeaders("gemini_embed", alias, apiKey, resp.Header)

	if resp.StatusCode != 200 {
		respBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("gemini API error: %s", string(respBytes))
	}

	var res struct {
		Embedding struct {
			Values []float32 `json:"values"`
		} `json:"embedding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}
	return res.Embedding.Values, nil
}

// Hàm hỗ trợ format vector float32 thành chuỗi Array format cho pgvector PostgreSQL
func FloatSliceToVectorString(fl []float32) string {
	strs := make([]string, len(fl))
	for i, f := range fl {
		strs[i] = fmt.Sprintf("%f", f)
	}
	return "[" + strings.Join(strs, ",") + "]"
}

func SplitIntoChunks(text string, chunkSize int, overlap int) []string {
	// Đây là hàm dummy split text vì không có thư viện cắt text tiếng việt chuyên dụng
	// Thực tế nên split theo câu hoặc \n\n. Ở đây tạm slice words.
	words := strings.Fields(text)
	var chunks []string

	i := 0
	for i < len(words) {
		end := i + chunkSize
		if end > len(words) {
			end = len(words)
		}
		chunk := strings.Join(words[i:end], " ")
		chunks = append(chunks, chunk)
		if end == len(words) {
			break
		}
		i += chunkSize - overlap
	}

	if len(chunks) == 0 && text != "" {
		chunks = append(chunks, text)
	}
	return chunks
}


// GenerateEmbedding là wrapper sử dụng pool để lấy vector embedding
func GenerateEmbedding(ctx context.Context, text string) ([]float32, error) {
	if GeminiEmbedPool == nil {
		return nil, fmt.Errorf("GeminiEmbedPool chưa được khởi tạo")
	}
	return GeminiEmbedPool.EmbedWithRetry(text, CallGeminiAPI)
}
