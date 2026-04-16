package utils

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// StreamOpenAIChat là adapter chung cho các provider dùng chuẩn OpenAI (Groq, Cerebras, Mistral, OpenRouter)
func StreamOpenAIChat(c *gin.Context, cfg AIProviderConfig, messages []ChatMessage) (string, error) {
	// Làm sạch UTF-8 cho messages
	sanitizedMessages := make([]ChatMessage, len(messages))
	for i, m := range messages {
		sanitizedMessages[i] = ChatMessage{
			Role:    m.Role,
			Content: strings.ToValidUTF8(m.Content, ""),
		}
	}

	reqBody := map[string]interface{}{
		"model":    cfg.Model,
		"messages": sanitizedMessages,
		"stream":   true,
	}

	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("lỗi đóng gói JSON cho %s: %v", cfg.Type, err)
	}

	key, alias := cfg.Pool.GetKey()
	if key == "" {
		return "", fmt.Errorf("không có API Key cho pool %s", cfg.Type)
	}

	apiURL := fmt.Sprintf("%s/chat/completions", strings.TrimSuffix(cfg.BaseURL, "/"))
	req, _ := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonBytes))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")

	log.Printf("🚀 %s Stream: Khởi tạo với %s cho model %s", cfg.Type, alias, cfg.Model)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("%s API failed with status %s", cfg.Type, resp.Status)
	}

	// Cập nhật Quota Status
	UpdateKeyStatusFromHeaders(string(cfg.Type), alias, key, resp.Header)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return "", errors.New("streaming unsupported")
	}

	scanner := bufio.NewScanner(resp.Body)
	var fullAnswer string

	for scanner.Scan() {
		line := scanner.Text()
		if len(line) < 6 || !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := line[6:]
		if data == "[DONE]" {
			break
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}

		if err := json.Unmarshal([]byte(data), &chunk); err == nil && len(chunk.Choices) > 0 {
			content := chunk.Choices[0].Delta.Content
			if content != "" {
				fullAnswer += content
				
				tokenPayload, _ := json.Marshal(map[string]string{"token": content})
				fmt.Fprintf(c.Writer, "event: token\ndata: %s\n\n", string(tokenPayload))
				flusher.Flush()
			}
		}
	}

	return fullAnswer, nil
}

// ChatOpenAINonStream là adapter gọi chat không stream
func ChatOpenAINonStream(cfg AIProviderConfig, messages []ChatMessage) (string, error) {
	// Làm sạch UTF-8 cho messages
	sanitizedMessages := make([]ChatMessage, len(messages))
	for i, m := range messages {
		sanitizedMessages[i] = ChatMessage{
			Role:    m.Role,
			Content: strings.ToValidUTF8(m.Content, ""),
		}
	}

	reqBody := map[string]interface{}{
		"model":    cfg.Model,
		"messages": sanitizedMessages,
		"stream":   false,
	}

	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("lỗi đóng gói JSON cho %s: %v", cfg.Type, err)
	}

	key, alias := cfg.Pool.GetKey()
	if key == "" {
		return "", fmt.Errorf("không có API Key cho pool %s", cfg.Type)
	}

	apiURL := fmt.Sprintf("%s/chat/completions", strings.TrimSuffix(cfg.BaseURL, "/"))
	req, _ := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonBytes))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")

	log.Printf("🤖 %s Non-Stream: Sử dụng %s cho model %s", cfg.Type, alias, cfg.Model)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("%s API failed with status %s", cfg.Type, resp.Status)
	}

	UpdateKeyStatusFromHeaders(string(cfg.Type), alias, key, resp.Header)

	var res struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}

	if len(res.Choices) > 0 {
		return res.Choices[0].Message.Content, nil
	}
	return "", nil
}
