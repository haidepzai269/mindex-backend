package routes

import (
	"mindex-backend/controllers"
	"mindex-backend/middleware"

	"github.com/gin-gonic/gin"
)

func RegisterAdminRoutes(r *gin.RouterGroup) {
	admin := r.Group("/admin")
	admin.Use(middleware.AuthMiddleware(), middleware.RequireAdmin())
	{
		// System Health
		admin.GET("/system", controllers.AdminSystemHealth)
		
		// Token Monitor
		admin.GET("/tokens", controllers.AdminTokenOverview)
		
		// Chat Audit
		admin.GET("/chats", controllers.AdminListChats)
		admin.GET("/chats/:session_id", controllers.AdminGetChatDetail)
		admin.PATCH("/chats/:session_id/flag", controllers.AdminFlagChat)
		
		// API Key Quota status (Multi-tier)
		admin.GET("/quota", controllers.AdminQuotaSummary)
		admin.GET("/keys/status", controllers.AdminKeyStatus)
		admin.GET("/keys/stream", controllers.AdminKeyStream)

		// Billings
		admin.GET("/billings", controllers.AdminGetBillingsStats)
		admin.POST("/billings/prices", controllers.AdminUpdateBillingPrices)
	}
}
