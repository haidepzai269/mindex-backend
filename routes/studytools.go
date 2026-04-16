package routes

import (
	"mindex-backend/controllers"
	"mindex-backend/middleware"

	"github.com/gin-gonic/gin"
)

// RegisterStudyToolsRoutes đăng ký các route cho P1 Study Tools
func RegisterStudyToolsRoutes(rg *gin.RouterGroup) {
	study := rg.Group("/study")
	study.Use(middleware.AuthMiddleware())
	{
		// Flashcard endpoints
		study.POST("/docs/:doc_id/flashcards/generate", controllers.GenerateFlashcards)
		study.GET("/docs/:doc_id/flashcards", controllers.GetFlashcardSets)
		study.GET("/flashcards/:set_id", controllers.GetFlashcards)
		study.PATCH("/flashcards/:card_id/mark", controllers.MarkFlashcard)
		study.GET("/flashcards/:set_id/export", controllers.ExportFlashcardsCSV) // ?format=csv

		// Quiz endpoints
		study.POST("/docs/:doc_id/quiz/generate", controllers.GenerateQuiz)
		study.GET("/quiz/:quiz_id", controllers.GetQuiz)
		study.POST("/quiz/:quiz_id/submit", controllers.SubmitQuiz)

		// Mastery & Progress
		study.GET("/docs/:doc_id/mastery", controllers.GetMasteryScore)
	}
}
