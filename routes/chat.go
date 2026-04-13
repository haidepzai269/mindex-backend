package routes

import (
	"mindex-backend/controllers"
	"mindex-backend/middleware"

	"github.com/gin-gonic/gin"
)

func RegisterChatRoutes(rg *gin.RouterGroup) {
	chat := rg.Group("/chat")

	chat.Use(middleware.AuthMiddleware())

	chat.POST("/sessions", controllers.CreateSession)
	chat.GET("/sessions/:session_id/messages", controllers.GetSessionMessages)
	chat.GET("/sessions/active/:doc_id", controllers.GetActiveSession)
	chat.POST("/message", controllers.ChatMessage)
	chat.GET("/message", controllers.ChatMessage)
}
