package routes

import (
	"time"

	"mindex-backend/controllers"
	"mindex-backend/middleware"

	"github.com/gin-gonic/gin"
)

func RegisterChatRoutes(rg *gin.RouterGroup) {
	chat := rg.Group("/chat")
	chat.Use(middleware.AuthMiddleware())

	chat.POST("/sessions", controllers.CreateSession)
	chat.PATCH("/sessions/:session_id", controllers.RenameSession)
	chat.DELETE("/sessions/:session_id", controllers.DeleteSession)
	chat.GET("/sessions/:session_id/messages", controllers.GetSessionMessages)
	chat.GET("/sessions/active/:doc_id", controllers.GetActiveSession)

	// AI endpoints bị giới hạn 30 req/phút per user
	ai := chat.Group("/ai")
	{
		ai.POST("/sessions", controllers.CreateGlobalAISession)
		ai.GET("/sessions", controllers.ListGlobalAISessions)
		ai.GET("/sessions/active", controllers.GetActiveGlobalAISession)
		ai.GET("/sessions/:session_id/messages", controllers.GetGlobalAISessionMessages)
		ai.PATCH("/sessions/:session_id", controllers.RenameGlobalAISession)
		ai.DELETE("/sessions/:session_id", controllers.DeleteGlobalAISession)
		ai.POST("/message", middleware.RateLimit("ai_global_chat", 30, time.Minute), controllers.GlobalAIMessage)
	}

	chat.POST("/message", middleware.RateLimit("ai_chat", 30, time.Minute), controllers.ChatMessage)
	chat.GET("/message", middleware.RateLimit("ai_chat", 30, time.Minute), controllers.ChatMessage)
}
