package routes

import (
	"time"

	"mindex-backend/controllers"
	"mindex-backend/middleware"

	"github.com/gin-gonic/gin"
)

func RegisterToolRoutes(rg *gin.RouterGroup) {
	toolsGroup := rg.Group("/tools")
	toolsGroup.Use(middleware.AuthMiddleware())

	news := toolsGroup.Group("/news")
	{
		news.GET("/more", middleware.RateLimit("news_pagination", 10, time.Hour), controllers.GetMoreNews)
	}
}
