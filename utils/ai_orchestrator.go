package utils

import (
	"fmt"
	"log"
	"strings"

	"github.com/gin-gonic/gin"
)

// ServiceType định nghĩa loại dịch vụ AI
type ServiceType string

const (
	ServiceChat     ServiceType = "CHAT"
	ServiceSummary  ServiceType = "SUMMARY"
	ServiceClassify ServiceType = "CLASSIFY"
	ServiceSearch   ServiceType = "SEARCH"
)

// ProviderType định nghĩa loại Provider
type ProviderType string

const (
	ProviderGroq       ProviderType = "groq"
	ProviderGemini     ProviderType = "gemini"
	ProviderCerebras   ProviderType = "cerebras"
	ProviderMistral    ProviderType = "mistral"
	ProviderOpenRouter ProviderType = "openrouter"
	ProviderHF         ProviderType = "huggingface"
)

// AIProviderConfig chứa thông tin cấu hình cho một Provider cụ thể trong một Service
type AIProviderConfig struct {
	Type      ProviderType
	Model     string
	Pool      *ApiKeyPool
	IsOpenAI  bool   // Nếu true, dùng OpenAI Adapter
	BaseURL   string // Dùng cho OpenAI-compatible APIs
}

// AIOrchestrator quản lý việc điều phối các Provider
type AIOrchestrator struct {
	Priorities map[ServiceType][]AIProviderConfig
}

var AI *AIOrchestrator

func init() {
	// Khởi tạo thực tế sẽ được gọi sau khi các Pool đã sẵn sàng (thường là trong main hoặc một hàm Init riêng)
}

// InitOrchestrator thiết lập thứ tự ưu tiên fallback theo đúng yêu cầu người dùng
func InitOrchestrator() {
	AI = &AIOrchestrator{
		Priorities: make(map[ServiceType][]AIProviderConfig),
	}

	// 1. CHAT (RAG)
	AI.Priorities[ServiceChat] = []AIProviderConfig{
		{Type: ProviderGroq, Model: "llama-3.3-70b-versatile", Pool: GroqPool, IsOpenAI: true, BaseURL: "https://api.groq.com/openai/v1"},
		{Type: ProviderCerebras, Model: "qwen-3-235b-a22b-instruct-2507", Pool: CerebrasPool, IsOpenAI: true, BaseURL: "https://api.cerebras.ai/v1"},
		{Type: ProviderMistral, Model: "mistral-small-latest", Pool: MistralPool, IsOpenAI: true, BaseURL: "https://api.mistral.ai/v1"},
		{Type: ProviderOpenRouter, Model: "google/gemini-2.0-flash-exp:free", Pool: OpenRouterPool, IsOpenAI: true, BaseURL: "https://openrouter.ai/api/v1"},
	}

	// 2. TÓM TẮT (SUMMARY)
	AI.Priorities[ServiceSummary] = []AIProviderConfig{
		{Type: ProviderGemini, Model: "gemini-2.5-flash-lite", Pool: GeminiChatPool, IsOpenAI: false},
		{Type: ProviderMistral, Model: "mistral-small-latest", Pool: MistralPool, IsOpenAI: true, BaseURL: "https://api.mistral.ai/v1"},
		{Type: ProviderGroq, Model: "llama-3.3-70b-versatile", Pool: GroqPool, IsOpenAI: true, BaseURL: "https://api.groq.com/openai/v1"},
		{Type: ProviderOpenRouter, Model: "deepseek/deepseek-r1:free", Pool: OpenRouterPool, IsOpenAI: true, BaseURL: "https://openrouter.ai/api/v1"},
	}

	// 3. PHÂN LOẠI (CLASSIFY)
	AI.Priorities[ServiceClassify] = []AIProviderConfig{
		{Type: ProviderGemini, Model: "gemini-2.5-flash-lite", Pool: GeminiChatPool, IsOpenAI: false},
		{Type: ProviderCerebras, Model: "qwen-3-235b-a22b-instruct-2507", Pool: CerebrasPool, IsOpenAI: true, BaseURL: "https://api.cerebras.ai/v1"},
		{Type: ProviderGroq, Model: "llama-3.3-70b-versatile", Pool: GroqPool, IsOpenAI: true, BaseURL: "https://api.groq.com/openai/v1"},
		{Type: ProviderHF, Model: "facebook/bart-large-mnli", Pool: HFPool, IsOpenAI: false},
	}

	// 4. TÌM KIẾM (SEARCH)
	AI.Priorities[ServiceSearch] = []AIProviderConfig{
		{Type: ProviderGroq, Model: "llama-3.3-70b-versatile", Pool: GroqPool, IsOpenAI: true, BaseURL: "https://api.groq.com/openai/v1"},
		{Type: ProviderCerebras, Model: "llama3.1-8b", Pool: CerebrasPool, IsOpenAI: true, BaseURL: "https://api.cerebras.ai/v1"},
		{Type: ProviderMistral, Model: "mistral-small-latest", Pool: MistralPool, IsOpenAI: true, BaseURL: "https://api.mistral.ai/v1"},
		{Type: ProviderHF, Model: "meta-llama/Llama-3.2-3B-Instruct", Pool: HFPool, IsOpenAI: false},
	}
}

// ChatStream thực hiện gọi stream chat với cơ chế fallback
func (o *AIOrchestrator) ChatStream(service ServiceType, c *gin.Context, messages []ChatMessage) (string, ProviderType, error) {
	configs := o.Priorities[service]
	var lastErr error

	for _, cfg := range configs {
		// Kiểm tra pool có sẵn không
		if cfg.Pool == nil || len(cfg.Pool.keys) == 0 {
			continue
		}

		log.Printf("🌐 [Orchestrator] [%s] Thử Provider: %s (Model: %s)", service, cfg.Type, cfg.Model)

		var answer string
		var err error

		if cfg.IsOpenAI {
			answer, err = StreamOpenAIChat(c, cfg, messages)
		} else if cfg.Type == ProviderGemini {
			answer, err = StreamGeminiChatWithModel(c, messages, cfg.Model)
		} else if cfg.Type == ProviderHF {
			// HuggingFace for Search rewrite usually non-stream, 
			// but we can wrap it if needed. For now skip as primary chat.
			continue
		}

		if err == nil {
			log.Printf("✅ [Orchestrator] [%s] Thành công với %s", service, cfg.Type)
			return answer, cfg.Type, nil
		}

		lastErr = err
		log.Printf("⚠️ [Orchestrator] [%s] Provider %s lỗi: %v. Đang fallback...", service, cfg.Type, err)
		
		// Nếu client đã ngắt kết nối thì dừng luôn
		if strings.Contains(err.Error(), "broken pipe") || strings.Contains(err.Error(), "context canceled") {
			return answer, cfg.Type, err
		}
	}

	return "", "", fmt.Errorf("tất cả các provider cho %s đều thất bại: %v", service, lastErr)
}

// ChatNonStream thực hiện gọi chat không stream (dùng cho Summary JSON hoặc Classify)
func (o *AIOrchestrator) ChatNonStream(service ServiceType, messages []ChatMessage) (string, ProviderType, error) {
	configs := o.Priorities[service]
	var lastErr error

	for _, cfg := range configs {
		if cfg.Pool == nil || len(cfg.Pool.keys) == 0 {
			continue
		}

		log.Printf("🌐 [Orchestrator-NS] [%s] Thử Provider: %s", service, cfg.Type)

		var answer string
		var err error

		if cfg.IsOpenAI {
			answer, err = ChatOpenAINonStream(cfg, messages)
		} else if cfg.Type == ProviderGemini {
			answer, err = GeminiChatNonStreamWithModel(messages, cfg.Model)
		} else if cfg.Type == ProviderHF {
            if service == ServiceClassify {
                answer, err = ClassifyPersonaWithHF(messages[len(messages)-1].Content)
            } else if service == ServiceSearch {
                answer, err = RewriteQueryForSearch(messages[len(messages)-1].Content)
            }
		}

		if err == nil {
			return answer, cfg.Type, nil
		}

		lastErr = err
		log.Printf("⚠️ [Orchestrator-NS] [%s] Provider %s lỗi: %v", service, cfg.Type, err)
	}

	return "", "", fmt.Errorf("tất cả các provider đều thất bại: %v", lastErr)
}
