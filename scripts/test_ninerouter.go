package main

import (
	"fmt"
	"log"
	"mindex-backend/config"
	"mindex-backend/utils"
)

func main() {
	// 1. Load configuration from .env
	fmt.Println("🔍 Đang tải cấu hình từ .env...")
	config.LoadConfig()

	if len(config.Env.NineRouterKeys) == 0 {
		log.Fatal("❌ Lỗi: Không tìm thấy NINEROUTER_API_KEYS trong file .env")
	}

	// 2. Initialize NineRouter API Key Pool
	fmt.Printf("📦 Đang khởi tạo NineRouter Pool với %d keys...\n", len(config.Env.NineRouterKeys))
	utils.NineRouterPool = utils.NewApiKeyPool("ninerouter", config.Env.NineRouterKeys)

	// 3. Prepare AI Provider Configuration
	cfg := utils.AIProviderConfig{
		Type:     utils.ProviderNineRouter,
		Model:    config.Env.NineRouterModel,
		Pool:     utils.NineRouterPool,
		IsOpenAI: true,
		BaseURL:  config.Env.NineRouterBaseURL,
	}

	// 4. Create test message (Simulating Subsequent Message logic)
	systemPrompt := "Bạn là một trợ lý hữu ích."
	brandedPrompt := utils.ApplyMindexBranding(systemPrompt, false) // Giả lập tin nhắn thứ 2 trở đi

	messages := []utils.ChatMessage{
		{Role: "system", Content: brandedPrompt},
		{Role: "user", Content: "Bạn có thể tóm tắt lại các dịch vụ bạn cung cấp không?"},
	}

	fmt.Println("🌐 Đang gửi yêu cầu tới NineRouter (OpenAI Adapter)...")
	fmt.Printf("🔗 URL: %s\n", cfg.BaseURL)
	fmt.Printf("🤖 Model: %s\n", cfg.Model)

	// 5. Call NineRouter via OpenAI Adapter
	answer, err := utils.ChatOpenAINonStream(cfg, messages)
	
	if err != nil {
		fmt.Printf("\n❌ THẤT BẠI: Lỗi giao tiếp với NineRouter!\n")
		fmt.Printf("⚠️ Chi tiết lỗi: %v\n", err)
		
		if (err.Error() == "ninerouter API failed with status 401 Unauthorized") {
			fmt.Println("👉 Giải pháp: Kiểm tra lại NINEROUTER_API_KEYS trong .env. Key có thể đã hết hạn hoặc không đúng.")
		} else if (err.Error() == "ninerouter API failed with status 404 Not Found") {
			fmt.Println("👉 Giải pháp: Kiểm tra lại NINEROUTER_BASE_URL. Có thể endpoint /v1/chat/completions không đúng.")
		}
		return
	}

	// 6. Success
	fmt.Println("\n✅ THÀNH CÔNG: Kết nối tới NineRouter hoạt động tốt!")
	fmt.Printf("💬 Câu trả lời từ AI: %s\n", answer)
}
