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
	chat.DELETE("/sessions/:session_id/messages/:message_id", middleware.RateLimit("chat_message_mutation", 60, time.Minute), controllers.DeleteMessage)
	chat.POST("/sessions/:session_id/messages/:message_id/restore", middleware.RateLimit("chat_message_mutation", 60, time.Minute), controllers.RestoreMessage)
	chat.GET("/sessions/active/:doc_id", controllers.GetActiveSession)
	chat.POST("/attachments/image", middleware.RateLimit("chat_image_upload", 20, time.Minute), controllers.UploadChatImageAttachment)
	chat.POST("/attachments/video", middleware.RateLimit("chat_video_upload", 5, time.Minute), controllers.UploadChatVideoAttachment)
	chat.GET("/attachments/video/quota", middleware.RateLimit("video_quota_check", 30, time.Minute), controllers.GetVideoUploadQuota)
	chat.GET("/attachments/video/:attachment_id/status", controllers.GetVideoAttachmentStatus)

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
		ai.POST("/tools/confirm", middleware.RateLimit("ai_tool_confirm", 30, time.Minute), controllers.ConfirmToolCall)
	}

	chat.POST("/message", middleware.RateLimit("ai_chat", 30, time.Minute), controllers.ChatMessage)
	chat.GET("/message", middleware.RateLimit("ai_chat", 30, time.Minute), controllers.ChatMessage)
}
