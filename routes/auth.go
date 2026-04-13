package routes

import (
	"mindex-backend/controllers"
	"mindex-backend/middleware"

	"github.com/gin-gonic/gin"
)

func RegisterAuthRoutes(rg *gin.RouterGroup) {
	auth := rg.Group("/auth")

	// Public routes
	auth.POST("/register", controllers.Register)
	auth.POST("/login", controllers.Login)
	auth.POST("/google", controllers.GoogleLogin)
	auth.POST("/refresh", controllers.Refresh)
	auth.POST("/forgot-password/send-otp", controllers.ForgotPasswordSendOTP)
	auth.POST("/forgot-password/reset", controllers.ResetPassword)
	auth.GET("/onboarding-personas", controllers.GetOnboardingPersonas)

	// Protected routes
	auth.Use(middleware.AuthMiddleware())
	auth.GET("/me", controllers.GetMe)
	auth.PATCH("/me/profile", controllers.UpdateProfile)
	auth.POST("/me/send-otp", controllers.SendPasswordOTP)
	auth.PATCH("/me/persona", controllers.UpdatePersona)
	auth.POST("/me/change-password", controllers.ChangePassword)
	auth.POST("/logout", controllers.Logout)
}
