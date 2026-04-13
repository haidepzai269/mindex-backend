package routes

import (
	"mindex-backend/controllers"
	"mindex-backend/middleware"

	"github.com/gin-gonic/gin"
)

func RegisterShareRoutes(rg *gin.RouterGroup) {
	// Public: Xem shared link (không cần đăng nhập)
	rg.GET("/public/shared/:link_id", controllers.GetSharedLink)

	// Protected: Tạo shared link (phải đăng nhập)
	share := rg.Group("/documents/:id")
	share.Use(middleware.AuthMiddleware())
	share.POST("/share", controllers.CreateSharedLink)
}
