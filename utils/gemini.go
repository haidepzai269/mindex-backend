package utils

import (
	"context"
	"fmt"
	"log"
	"mindex-backend/config"
    "mindex-backend/utils/quota"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

// GeminiChatNonStream gửi yêu cầu tới Gemini và trả về nội dung hoàn chỉnh (Default model)
func GeminiChatNonStream(messages []ChatMessage) (string, error) {
	return GeminiChatNonStreamWithModel(messages, "gemini-1.5-flash")
}

// GeminiChatNonStreamWithModel gửi yêu cầu tới Gemini với model cụ thể
func GeminiChatNonStreamWithModel(messages []ChatMessage, modelID string) (string, error) {
	ctx := context.Background()
	
	// Xoay vòng Key từ Pool
	var lastErr error
	numKeys := len(config.Env.GeminiChatKeys)
	if numKeys == 0 {
		return "", fmt.Errorf("không tìm thấy Gemini API Key nào trong cấu hình")
	}

	for i := 0; i < numKeys; i++ {
		key, alias := GeminiChatPool.GetKey()
		// Wrap HTTP Client để bắt Header quota
		httpClient := NewQuotaHttpClient("gemini", alias, key)
		client, err := genai.NewClient(ctx, option.WithAPIKey(key), option.WithHTTPClient(httpClient))
		if err != nil {
			lastErr = err
			continue
		}
		defer client.Close()

		model := client.GenerativeModel(modelID)

		// Tách System Instruction và chuyển đổi Role
		var systemPrompt string
		var genaiMsgs []*genai.Content

		for _, m := range messages {
			if m.Role == "system" {
				systemPrompt += m.Content + "\n"
			} else {
				role := "user"
				if m.Role == "assistant" || m.Role == "model" {
					role = "model"
				}
				genaiMsgs = append(genaiMsgs, &genai.Content{
					Role:  role,
					Parts: []genai.Part{genai.Text(m.Content)},
				})
			}
		}

		if systemPrompt != "" {
			model.SystemInstruction = &genai.Content{
				Parts: []genai.Part{genai.Text(systemPrompt)},
			}
		}

		// Xử lý gửi yêu cầu
		var resp *genai.GenerateContentResponse
		if len(genaiMsgs) == 0 {
			return "", fmt.Errorf("không có nội dung yêu cầu (messages empty)")
		}

		if len(genaiMsgs) == 1 && genaiMsgs[0].Role == "user" {
			resp, err = model.GenerateContent(ctx, genaiMsgs[0].Parts...)
		} else {
			cs := model.StartChat()
			// History phải xen kẽ User/Model
			if len(genaiMsgs) > 1 {
				cs.History = genaiMsgs[:len(genaiMsgs)-1]
			}
			resp, err = cs.SendMessage(ctx, genaiMsgs[len(genaiMsgs)-1].Parts...)
		}

		if err != nil {
			log.Printf("⚠️ Gemini Error with %s: %v", alias, err)
			lastErr = err
			// Nếu lỗi 429 hoặc quota, thử key tiếp theo
			if strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), "quota") {
				time.Sleep(1500 * time.Millisecond)
				continue
			}
			return "", err
		}

			if len(resp.Candidates) > 0 {
				var result strings.Builder
				for _, part := range resp.Candidates[0].Content.Parts {
					if text, ok := part.(genai.Text); ok {
						result.WriteString(string(text))
					}
				}

                // Ghi nhận quota sau khi thành công
                if quota.GlobalTracker != nil {
                    tokens := int64(resp.UsageMetadata.TotalTokenCount)
                    quota.GlobalTracker.RecordCall(key, tokens)
                }

				return result.String(), nil
			}
	}

	return "", fmt.Errorf("Gemini failed sau khi thử %d keys: %v", numKeys, lastErr)
}

// StreamGeminiChat gửi yêu cầu tới Gemini và stream kết quả về Gin Context (Default model)
func StreamGeminiChat(c *gin.Context, messages []ChatMessage) (string, error) {
	return StreamGeminiChatWithModel(c, messages, "gemini-1.5-flash")
}

// StreamGeminiChatWithModel gửi yêu cầu tới Gemini và stream với model cụ thể
func StreamGeminiChatWithModel(c *gin.Context, messages []ChatMessage, modelID string) (string, error) {
	ctx := context.Background()
	numKeys := len(config.Env.GeminiChatKeys)
	if numKeys == 0 {
		return "", fmt.Errorf("không có API Key")
	}

	var lastErr error
	for i := 0; i < numKeys; i++ {
		key, alias := GeminiChatPool.GetKey()
		
		// Wrap HTTP Client để bắt Header quota
		httpClient := NewQuotaHttpClient("gemini", alias, key)
		client, err := genai.NewClient(ctx, option.WithAPIKey(key), option.WithHTTPClient(httpClient))
		if err != nil {
			lastErr = err
			continue
		}
		defer client.Close()

		log.Printf("🚀 Gemini Stream: Khởi tạo với %s (lần thử %d/%d) model %s", alias, i+1, numKeys, modelID)
		model := client.GenerativeModel(modelID)

		var systemPrompt string
		var genaiMsgs []*genai.Content

		for _, m := range messages {
			if m.Role == "system" {
				systemPrompt += m.Content + "\n"
			} else {
				role := "user"
				if m.Role == "assistant" || m.Role == "model" {
					role = "model"
				}
				genaiMsgs = append(genaiMsgs, &genai.Content{
					Role:  role,
					Parts: []genai.Part{genai.Text(m.Content)},
				})
			}
		}

		if systemPrompt != "" {
			model.SystemInstruction = &genai.Content{
				Parts: []genai.Part{genai.Text(systemPrompt)},
			}
		}

		if len(genaiMsgs) == 0 {
			return "", fmt.Errorf("no messages to send")
		}

		cs := model.StartChat()
		if len(genaiMsgs) > 1 {
			cs.History = genaiMsgs[:len(genaiMsgs)-1]
		}
		
		iter := cs.SendMessageStream(ctx, genaiMsgs[len(genaiMsgs)-1].Parts...)
		
		flusher, _ := c.Writer.(http.Flusher)
		var fullAnswer strings.Builder
		contentStarted := false

		for {
			resp, err := iter.Next()
			if err == iterator.Done {
                // Khi kết thúc stream, ghi nhận quota (số lượng token ước tính)
                if quota.GlobalTracker != nil {
                    // Đối với stream, chúng ta ghi nhận 1 request, 
                    // số token có thể ước lượng hoặc lấy từ response cuối nếu có
                    quota.GlobalTracker.RecordCall(key, 0) 
                }
				return fullAnswer.String(), nil
			}
			if err != nil {
				log.Printf("❌ Gemini Stream Error with %s: %v", alias, err)
				
				// Nếu lỗi xảy ra TRƯỚC khi bắt đầu stream dữ liệu và là lỗi có thể thử lại
				if !contentStarted && (strings.Contains(err.Error(), "503") || strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), "quota")) {
					log.Printf("⚠️ Đang thử lại với key tiếp theo do lỗi transient: %v", err)
					lastErr = err
					time.Sleep(1500 * time.Millisecond)
					break // Thoát vòng lặp 'for {}' để sang key tiếp theo trong 'for i'
				}
				
				return fullAnswer.String(), err
			}

			if len(resp.Candidates) > 0 {
				contentStarted = true
				for _, part := range resp.Candidates[0].Content.Parts {
					if text, ok := part.(genai.Text); ok {
						content := string(text)
						fullAnswer.WriteString(content)
						
						// Format tokens tương đồng với Groq SSE
						fmt.Fprintf(c.Writer, "event: token\ndata: {\"token\": %q}\n\n", content)
						flusher.Flush()
					}
				}
			}
		}
	}

	return "", fmt.Errorf("Gemini Stream failed sau khi thử %d keys: %v", numKeys, lastErr)
}
