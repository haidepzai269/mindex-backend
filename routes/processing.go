package routes

import (
	"mindex-backend/controllers"
	"mindex-backend/middleware"

	"github.com/gin-gonic/gin"
)

func RegisterProcessingRoutes(rg *gin.RouterGroup) {
	processing := rg.Group("/processing")

	// API yêu cầu đăng nhập
	processing.Use(middleware.AuthMiddleware())

	processing.POST("/presign", controllers.PresignUpload)
	processing.POST("/upload", controllers.InitiateUpload)
	processing.GET("/status/:id", controllers.GetProcessingStatus)
}
