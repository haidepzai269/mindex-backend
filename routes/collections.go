package routes

import (
	"mindex-backend/controllers"
	"mindex-backend/middleware"

	"github.com/gin-gonic/gin"
)

func RegisterCollectionRoutes(r *gin.RouterGroup) {
	collections := r.Group("/collections")
	collections.Use(middleware.AuthMiddleware())
	{
		collections.POST("", controllers.CreateCollection)
		collections.GET("", controllers.ListCollections)
		collections.GET("/:id", controllers.GetCollectionDetail)
		collections.PATCH("/:id", controllers.UpdateCollection)
		collections.DELETE("/:id", controllers.DeleteCollection)
		
		collections.POST("/:id/documents", controllers.AddDocumentToCollection)
		collections.DELETE("/:id/documents/:doc_id", controllers.RemoveDocumentFromCollection)
	}
}
