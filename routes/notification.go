package routes

import (
	"mindex-backend/controllers"
	"mindex-backend/middleware"

	"github.com/gin-gonic/gin"
)

func RegisterNotificationRoutes(r *gin.RouterGroup) {
	notif := r.Group("/notifications")
	notif.Use(middleware.AuthMiddleware())
	{
		notif.GET("/stream", controllers.StreamNotifications)
		notif.GET("", controllers.GetNotifications)
		notif.PATCH("/:id/read", controllers.MarkNotificationRead)
		notif.DELETE("/:id", controllers.DeleteNotification)
	}
}

