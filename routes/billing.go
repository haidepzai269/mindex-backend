package routes

import (
	"mindex-backend/controllers"
	"mindex-backend/middleware"

	"github.com/gin-gonic/gin"
)

func RegisterBillingRoutes(r *gin.RouterGroup) {
	bill := r.Group("/billings")
	{
		// Public — PayOS gọi về, không cần auth
		bill.POST("/webhook", controllers.PayOSWebhook)

		// Cần có token để mua và lấy giá
		bill.GET("/packages", middleware.AuthMiddleware(), controllers.GetPackages)
		bill.POST("/create-payment-link", middleware.AuthMiddleware(), controllers.CreatePaymentLink)
		bill.GET("/verify", middleware.AuthMiddleware(), controllers.VerifyPaymentClient)
		bill.GET("/history", middleware.AuthMiddleware(), controllers.GetPaymentHistory)
	}
}
