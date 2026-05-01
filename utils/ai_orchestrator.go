package utils

import (
	"fmt"
	"log"
	"mindex-backend/config"
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
	ProviderNineRouter ProviderType = "ninerouter"
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

	// 1. CHAT (RAG) - Ưu tiên Mindex2 (Combo 10 LLMs), Fallback sang các Provider khác nếu lỗi
	AI.Priorities[ServiceChat] = []AIProviderConfig{
		{Type: ProviderNineRouter, Model: config.Env.NineRouterChatModel, Pool: NineRouterChatPool, IsOpenAI: true, BaseURL: config.Env.NineRouterBaseURL},
		{Type: ProviderGroq, Model: "llama-3.3-70b-versatile", Pool: GroqPool, IsOpenAI: true, BaseURL: "https://api.groq.com/openai/v1"},
		{Type: ProviderCerebras, Model: "qwen-3-235b-a22b-instruct-2507", Pool: CerebrasPool, IsOpenAI: true, BaseURL: "https://api.cerebras.ai/v1"},
		{Type: ProviderMistral, Model: "mistral-small-latest", Pool: MistralPool, IsOpenAI: true, BaseURL: "https://api.mistral.ai/v1"},
		{Type: ProviderOpenRouter, Model: "google/gemini-2.0-flash-exp:free", Pool: OpenRouterPool, IsOpenAI: true, BaseURL: "https://openrouter.ai/api/v1"},
	}

	// 2. TÓM TẮT (SUMMARY) - NineRouter lên vị trí số 1
	AI.Priorities[ServiceSummary] = []AIProviderConfig{
		{Type: ProviderNineRouter, Model: config.Env.NineRouterModel, Pool: NineRouterPool, IsOpenAI: true, BaseURL: config.Env.NineRouterBaseURL},
		{Type: ProviderGemini, Model: "gemini-2.5-flash-lite", Pool: GeminiChatPool, IsOpenAI: false},
		{Type: ProviderMistral, Model: "mistral-small-latest", Pool: MistralPool, IsOpenAI: true, BaseURL: "https://api.mistral.ai/v1"},
		{Type: ProviderGroq, Model: "llama-3.3-70b-versatile", Pool: GroqPool, IsOpenAI: true, BaseURL: "https://api.groq.com/openai/v1"},
		{Type: ProviderOpenRouter, Model: "deepseek/deepseek-r1:free", Pool: OpenRouterPool, IsOpenAI: true, BaseURL: "https://openrouter.ai/api/v1"},
	}

	// 3. PHÂN LOẠI (CLASSIFY) - NineRouter ở vị trí số 2
	AI.Priorities[ServiceClassify] = []AIProviderConfig{
		{Type: ProviderGemini, Model: "gemini-2.5-flash-lite", Pool: GeminiChatPool, IsOpenAI: false},
		{Type: ProviderNineRouter, Model: config.Env.NineRouterModel, Pool: NineRouterPool, IsOpenAI: true, BaseURL: config.Env.NineRouterBaseURL},
		{Type: ProviderCerebras, Model: "qwen-3-235b-a22b-instruct-2507", Pool: CerebrasPool, IsOpenAI: true, BaseURL: "https://api.cerebras.ai/v1"},
		{Type: ProviderGroq, Model: "llama-3.3-70b-versatile", Pool: GroqPool, IsOpenAI: true, BaseURL: "https://api.groq.com/openai/v1"},
		{Type: ProviderHF, Model: "facebook/bart-large-mnli", Pool: HFPool, IsOpenAI: false},
	}

	// 4. TÌM KIẾM (SEARCH) - NineRouter ở vị trí số 3
	AI.Priorities[ServiceSearch] = []AIProviderConfig{
		{Type: ProviderGroq, Model: "llama-3.3-70b-versatile", Pool: GroqPool, IsOpenAI: true, BaseURL: "https://api.groq.com/openai/v1"},
		{Type: ProviderCerebras, Model: "llama3.1-8b", Pool: CerebrasPool, IsOpenAI: true, BaseURL: "https://api.cerebras.ai/v1"},
		{Type: ProviderNineRouter, Model: config.Env.NineRouterModel, Pool: NineRouterPool, IsOpenAI: true, BaseURL: config.Env.NineRouterBaseURL},
		{Type: ProviderMistral, Model: "mistral-small-latest", Pool: MistralPool, IsOpenAI: true, BaseURL: "https://api.mistral.ai/v1"},
		{Type: ProviderHF, Model: "meta-llama/Llama-3.2-3B-Instruct", Pool: HFPool, IsOpenAI: false},
	}
}

// ChatStream thực hiện gọi stream chat với cơ chế fallback
func (o *AIOrchestrator) ChatStream(service ServiceType, c *gin.Context, messages []ChatMessage, modelOverride string) (string, ProviderType, error) {
	configs := o.Priorities[service]

	// Nếu là Chat và có modelOverride, ta ưu tiên NineRouter với model được yêu cầu
	if service == ServiceChat && modelOverride != "" {
		var modelToUse string
		if modelOverride == "Mindex-2" {
			modelToUse = config.Env.NineRouterChatModel // Model2
		} else {
			modelToUse = config.Env.NineRouterModel // Model1 (Mindex-1 hoặc mặc định)
		}

		// Tạo danh sách config mới với NineRouter model đúng ở đầu
		newConfigs := []AIProviderConfig{
			{Type: ProviderNineRouter, Model: modelToUse, Pool: NineRouterChatPool, IsOpenAI: true, BaseURL: config.Env.NineRouterBaseURL},
		}
		// Thêm các fallback còn lại (loại bỏ NineRouter cũ để tránh trùng lặp)
		for _, cfg := range configs {
			if cfg.Type != ProviderNineRouter {
				newConfigs = append(newConfigs, cfg)
			}
		}
		configs = newConfigs
	}

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
			continue
		}

		if err == nil {
			log.Printf("✅ [Orchestrator] [%s] Thành công với %s", service, cfg.Type)
			return answer, cfg.Type, nil
		}

		lastErr = err
		log.Printf("⚠️ [Orchestrator] [%s] Provider %s lỗi: %v. Đang fallback...", service, cfg.Type, err)
		
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
