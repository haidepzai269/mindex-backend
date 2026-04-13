package main

import (
	"fmt"
	"log"
	"mindex-backend/config"
	"mindex-backend/internal/persona"
	"mindex-backend/routes"
	"mindex-backend/utils"
    "mindex-backend/utils/quota"
	"mindex-backend/workers"
	"mindex-backend/internal/ws"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

func main() {
	// 1. Load config
	config.LoadConfig()

	// 2. Init Key Pools
	utils.GeminiChatPool = utils.NewApiKeyPool("gemini_chat", config.Env.GeminiChatKeys)
	utils.GeminiEmbedPool = utils.NewApiKeyPool("gemini_embed", config.Env.GeminiEmbedKeys)
	utils.GeminiPool = utils.GeminiChatPool // Default alias
	utils.GroqPool = utils.NewApiKeyPool("groq", config.Env.GroqKeys)
	utils.CerebrasPool = utils.NewApiKeyPool("cerebras", config.Env.CerebrasKeys)
	utils.MistralPool = utils.NewApiKeyPool("mistral", config.Env.MistralKeys)
	utils.OpenRouterPool = utils.NewApiKeyPool("openrouter", config.Env.OpenRouterKeys)
	utils.HFPool = utils.NewApiKeyPool("hf", config.Env.HuggingFaceKeys)
	
	log.Printf("Đã khởi tạo Gemini Pool: Chat (%d keys), Embed (%d keys)", len(config.Env.GeminiChatKeys), len(config.Env.GeminiEmbedKeys))
	log.Printf("Đã khởi tạo Groq Pool với %d keys", len(config.Env.GroqKeys))
	log.Printf("Đã khởi tạo Cerebras Pool với %d keys", len(config.Env.CerebrasKeys))
	log.Printf("Đã khởi tạo Mistral Pool với %d keys", len(config.Env.MistralKeys))
	log.Printf("Đã khởi tạo OpenRouter Pool với %d keys", len(config.Env.OpenRouterKeys))
	log.Printf("Đã khởi tạo HF Pool với %d keys", len(config.Env.HuggingFaceKeys))

	// 2b. Init AI Orchestrator
	utils.InitOrchestrator()

	// 3. Connect DB & Redis
	config.ConnectDB()
	defer config.CloseDB()

	config.ConnectRedis()
	defer config.CloseRedis()

    // 3c. Init Quota Tracker
    quota.InitTracker()
    
    // Register Keys to Tracker
    for i, k := range config.Env.GeminiChatKeys { quota.GlobalTracker.RegisterKey(k, quota.ProviderGemini, fmt.Sprintf("gemini_chat_%d", i+1)) }
    for i, k := range config.Env.GeminiEmbedKeys { quota.GlobalTracker.RegisterKey(k, quota.ProviderGemini, fmt.Sprintf("gemini_embed_%d", i+1)) }
    for i, k := range config.Env.GroqKeys { quota.GlobalTracker.RegisterKey(k, quota.ProviderGroq, fmt.Sprintf("groq_%d", i+1)) }
    for i, k := range config.Env.CerebrasKeys { quota.GlobalTracker.RegisterKey(k, quota.ProviderCerebras, fmt.Sprintf("cerebras_%d", i+1)) }
    for i, k := range config.Env.MistralKeys { quota.GlobalTracker.RegisterKey(k, quota.ProviderMistral, fmt.Sprintf("mistral_%d", i+1)) }
    for i, k := range config.Env.OpenRouterKeys { quota.GlobalTracker.RegisterKey(k, quota.ProviderOpenRouter, fmt.Sprintf("openrouter_%d", i+1)) }
    for i, k := range config.Env.HuggingFaceKeys { quota.GlobalTracker.RegisterKey(k, quota.ProviderHuggingFace, fmt.Sprintf("hf_%d", i+1)) }

	// 3b. Init Persona Cache
	if err := persona.Cache.Load(config.DB); err != nil {
		log.Fatalf("Failed to load persona prompts: %v", err)
	}

	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		for range ticker.C {
			if err := persona.Cache.Load(config.DB); err != nil {
				log.Printf("Failed to refresh persona cache: %v", err)
			}
		}
	}()

	// 4. Khởi chạy Background Workers
	workers.StartWorkerPool()
	workers.StartSweeper() // Chạy ngầm dọn dẹp lúc 3AM
	workers.StartExpirer() // Thông báo hết hạn realtime

	// 4b. Khởi chạy WebSocket Hub cho Feedback
	go ws.GlobalHub.Run()


	// 5. Init router
	r := gin.Default()

	// 6. Setup CORS chuyên sâu để cho phép Frontend (3000) gọi Backend (8080)
	r.Use(cors.New(cors.Config{
		AllowOrigins:     []string{"http://localhost:3000"},
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Accept", "Authorization", "X-Requested-With"},
		ExposeHeaders:    []string{"Content-Length"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}))

	// 6. Routes
	api := r.Group("/api/v1")
	{
		api.GET("/health", func(c *gin.Context) {
			c.JSON(200, gin.H{"status": "ok", "message": "Mindex API Server is running!"})
		})

		routes.RegisterAuthRoutes(api)
		routes.RegisterProcessingRoutes(api)
		routes.RegisterChatRoutes(api)
		routes.RegisterDocumentRoutes(api)
		routes.RegisterShareRoutes(api)
		routes.RegisterCollectionRoutes(api)
		routes.RegisterAdminRoutes(api)
		routes.RegisterNotificationRoutes(api)
		routes.RegisterFeedbackRoutes(api)
		routes.RegisterBillingRoutes(api)
	}

	// 8. Start server
	log.Printf("Starting server on port %s...", config.Env.Port)
	if err := r.Run(":" + config.Env.Port); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}
