package routes

import (
	"mindex-backend/controllers"
	"mindex-backend/middleware"

	"github.com/gin-gonic/gin"
)

func RegisterRoomRoutes(rg *gin.RouterGroup) {
	rooms := rg.Group("/rooms")
	rooms.Use(middleware.AuthMiddleware())
	{
		rooms.POST("", controllers.CreateRoom)
		rooms.GET("/my", controllers.GetMyRooms)
		rooms.GET("/info", controllers.GetRoomInfo)
		rooms.POST("/join", controllers.JoinRoom)
		
		rooms.GET("/:id", controllers.GetRoom)
		rooms.POST("/:id/leave", controllers.LeaveRoom)
		rooms.POST("/:id/close", controllers.CloseRoom)
		rooms.GET("/:id/docs", controllers.GetRoomDocs)
		rooms.POST("/:id/docs/link", controllers.LinkDocToRoom)
		rooms.DELETE("/:id/docs/:doc_id", controllers.UnlinkDocFromRoom)
		rooms.POST("/:id/kick/:user_id", controllers.KickMember)

		// WebSocket for group chat
		rooms.GET("/:id/ws", controllers.ConnectRoomWS)

		// REST: load thêm lịch sử chat cũ hơn (cursor-based)
		rooms.GET("/:id/history", controllers.GetRoomHistory)
	}
}
