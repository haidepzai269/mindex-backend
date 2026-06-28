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
		feedback.POST("/send-email", controllers.SendFeedbackEmail)

		// AI Response Rating (P0 — Quality Monitoring)
		feedback.POST("/rating", controllers.SubmitResponseRating)
		feedback.GET("/rating/:log_id", controllers.GetRatingByLogID)
	}

	// WebSocket route uses cookie/header auth from AuthMiddleware.
	rg.GET("/ws/feedback", middleware.AuthMiddleware(), controllers.ServeFeedbackWS)
}
