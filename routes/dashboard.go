package routes

import (
	"mindex-backend/controllers"
	"mindex-backend/middleware"

	"github.com/gin-gonic/gin"
)

func RegisterDashboardRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/dashboard")
	g.Use(middleware.AuthMiddleware())
	{
		g.GET("/metrics", controllers.GetDashboardMetrics)
		g.GET("/trends", controllers.GetDashboardTrends)
		g.GET("/highlights", controllers.GetDashboardHighlights)
	}
}
