package routes

import (
	"mindex-backend/controllers"
	"mindex-backend/middleware"

	"github.com/gin-gonic/gin"
)

func RegisterAnalyticsRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/analytics")
	g.Use(middleware.AuthMiddleware())
	{
		g.GET("/summary", controllers.GetAnalyticsSummary)
		g.GET("/activity", controllers.GetAnalyticsActivity)
	}
}

func RegisterGamificationRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/gamification")
	g.Use(middleware.AuthMiddleware())
	{
		g.GET("/badges", controllers.GetBadges)
		g.GET("/streak", controllers.GetStreak)
	}
}

func RegisterProfileRoutes(rg *gin.RouterGroup) {
	// Public profile — không cần auth
	rg.GET("/users/:id/profile", controllers.GetPublicProfile)
}
