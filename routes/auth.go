package routes

import (
	"time"

	"mindex-backend/controllers"
	"mindex-backend/middleware"

	"github.com/gin-gonic/gin"
)

func RegisterAuthRoutes(rg *gin.RouterGroup) {
	auth := rg.Group("/auth")

	// Public routes (login bị giới hạn 10 lần/phút per IP để chống brute-force)
	auth.POST("/register", controllers.Register)
	auth.POST("/login", middleware.RateLimit("login", 10, 60*time.Second), controllers.Login)
	auth.POST("/google", controllers.GoogleLogin)
	auth.POST("/refresh", controllers.Refresh)
	auth.POST("/forgot-password/send-otp", middleware.RateLimit("otp", 5, 60*time.Second), controllers.ForgotPasswordSendOTP)
	auth.POST("/forgot-password/reset", controllers.ResetPassword)
	auth.GET("/onboarding-personas", controllers.GetOnboardingPersonas)
	auth.GET("/check-email", controllers.CheckEmail)

	// Protected routes
	auth.Use(middleware.AuthMiddleware())
	auth.GET("/me", controllers.GetMe)
	auth.PATCH("/me/profile", controllers.UpdateProfile)
	auth.POST("/me/send-otp", controllers.SendPasswordOTP)
	auth.PATCH("/me/persona", controllers.UpdatePersona)
	auth.POST("/me/change-password", controllers.ChangePassword)
	auth.POST("/logout", controllers.Logout)
}
