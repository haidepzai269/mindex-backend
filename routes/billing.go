package routes

import (
	"mindex-backend/controllers"
	"mindex-backend/middleware"

	"github.com/gin-gonic/gin"
)

func RegisterBillingRoutes(r *gin.RouterGroup) {
	bill := r.Group("/billings")
	{
		// Cần có token để mua và lấy giá
		bill.GET("/packages", middleware.AuthMiddleware(), controllers.GetPackages)
		bill.POST("/create-payment-link", middleware.AuthMiddleware(), controllers.CreatePaymentLink)
		bill.GET("/verify", middleware.AuthMiddleware(), controllers.VerifyPaymentClient)
	}
}
