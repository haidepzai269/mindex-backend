package routes

import (
	"time"

	"mindex-backend/controllers"
	"mindex-backend/middleware"

	"github.com/gin-gonic/gin"
)

// RegisterStudyToolsRoutes đăng ký các route cho P1 Study Tools
func RegisterStudyToolsRoutes(rg *gin.RouterGroup) {
	study := rg.Group("/study")
	study.Use(middleware.AuthMiddleware())
	{
		// Flashcard endpoints — generate bị giới hạn 10 lần/phút/user (AI-heavy)
		study.POST("/docs/:doc_id/flashcards/generate",
			middleware.RateLimit("ai_generate", 10, time.Minute),
			controllers.GenerateFlashcards)
		study.GET("/docs/:doc_id/flashcards", controllers.GetFlashcardSets)
		study.GET("/flashcards/:set_id", controllers.GetFlashcards)
		study.GET("/flashcards/:set_id/due", controllers.GetDueFlashcards)
		study.PATCH("/flashcards/:card_id/mark", controllers.MarkFlashcard)
		study.GET("/flashcards/:set_id/export", controllers.ExportFlashcardsCSV) // ?format=csv

		// Quiz endpoints — generate bị giới hạn 10 lần/phút/user (AI-heavy)
		study.POST("/docs/:doc_id/quiz/generate",
			middleware.RateLimit("ai_generate", 10, time.Minute),
			controllers.GenerateQuiz)
		study.GET("/docs/:doc_id/quiz/history", controllers.GetQuizHistory)
		study.GET("/quiz/:quiz_id", controllers.GetQuiz)
		study.POST("/quiz/:quiz_id/submit", controllers.SubmitQuiz)

		// Attempt history
		study.GET("/attempts/:attempt_id", controllers.GetQuizAttempt)

		// Mastery & Progress
		study.GET("/docs/:doc_id/mastery", controllers.GetMasteryScore)

		// Study Planner
		study.POST("/plans", controllers.CreateStudyPlan)
		study.GET("/plans", controllers.GetStudyPlans)
		study.DELETE("/plans/:id", controllers.DeleteStudyPlan)

		// Mindmap endpoints
		study.POST("/docs/:doc_id/mindmap/generate",
			middleware.RateLimit("ai_generate", 10, time.Minute),
			controllers.GenerateMindmap)
		study.GET("/docs/:doc_id/mindmap", controllers.GetMindmap)

		// Audio Overview endpoints
		study.POST("/docs/:doc_id/audio/generate",
			middleware.RateLimit("ai_generate", 10, time.Minute),
			controllers.GenerateAudioOverview)
		study.GET("/docs/:doc_id/audio", controllers.GetAudioOverview)

		// Presentation (Slides & Video) endpoints
		study.POST("/docs/:doc_id/presentation/generate",
			middleware.RateLimit("ai_generate", 10, time.Minute),
			controllers.GeneratePresentation)
		study.GET("/docs/:doc_id/presentation", controllers.GetPresentation)
	}
}
