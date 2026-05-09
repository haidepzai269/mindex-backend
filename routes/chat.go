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
	chat.POST("/message", middleware.RateLimit("ai_chat", 30, time.Minute), controllers.ChatMessage)
	chat.GET("/message", middleware.RateLimit("ai_chat", 30, time.Minute), controllers.ChatMessage)
}
