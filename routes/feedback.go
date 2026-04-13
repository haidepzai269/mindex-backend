package routes

import (
	"mindex-backend/controllers"
	"mindex-backend/middleware"

	"github.com/gin-gonic/gin"
)

func RegisterFeedbackRoutes(rg *gin.RouterGroup) {
	feedback := rg.Group("/feedbacks")
	feedback.Use(middleware.AuthMiddleware())
	{
		feedback.GET("/sessions", controllers.GetFeedbackSessions)
		feedback.POST("/sessions", controllers.CreateFeedbackSession)
		feedback.GET("/sessions/:id/messages", controllers.GetFeedbackMessages)
	}

	// WebSocket route (Public because token is in query)
	rg.GET("/ws/feedback", controllers.ServeFeedbackWS)
}
