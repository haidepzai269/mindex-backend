package routes

import (
	"mindex-backend/controllers"
	"mindex-backend/middleware"
	"mindex-backend/workers"

	"github.com/gin-gonic/gin"
)

func RegisterDocumentRoutes(rg *gin.RouterGroup) {
	// ── Public Endpoints (không cần đăng nhập) ──
	rg.GET("/community/search", controllers.SearchCommunity)
	rg.GET("/community/documents/:id", controllers.GetCommunityDocumentDetail)

	// ── Protected Endpoints ──
	doc := rg.Group("/")
	doc.Use(middleware.AuthMiddleware())

	// Quản lý tài liệu cá nhân
	doc.GET("/documents", controllers.GetMyDocuments)
	doc.GET("/documents/:id", controllers.GetDocumentDetail)
	doc.PATCH("/documents/:id/pin", controllers.TogglePinDocument)
	doc.PATCH("/documents/:id/persona", controllers.UpdateDocumentPersona)
	doc.DELETE("/documents/:id", controllers.DeleteDocument)

	// Tóm tắt & Trích xuất
	doc.POST("/summary/quick", controllers.QuickSummary)
	doc.POST("/summary/detailed", controllers.DetailedSummary)
	doc.GET("/summary/cache/:id", controllers.GetCachedSummary)
	doc.POST("/extract/keywords", controllers.ExtractKeywords)
	doc.POST("/extract/timeline", controllers.ExtractTimeline)

	// Community Library (cần login)
	doc.PATCH("/community/documents/:id", controllers.AddCommunityLibrary)           // Share/Unshare
	doc.POST("/community/documents/:id/use", controllers.UseCommunityDocument)       // Thêm vào thư viện cá nhân
	doc.POST("/community/documents/:id/upvote", controllers.UpvoteCommunityDocument) // Bình chọn
	doc.GET("/community/my-contributions", controllers.GetMyContributions)           // Đóng góp của tôi

	// Lịch sử tìm kiếm cộng đồng
	doc.GET("/community/search/history", controllers.GetSearchHistory)
	doc.POST("/community/search/history", controllers.AddSearchHistory)
	doc.DELETE("/community/search/history/:id", controllers.DeleteSearchHistory)
	doc.DELETE("/community/search/history", controllers.ClearSearchHistory)

	// ── Admin Endpoints ──
	admin := rg.Group("/admin")
	admin.Use(middleware.AuthMiddleware(), middleware.RequireAdmin())

	// Kích hoạt thủ công Sweeper
	admin.POST("/sweeper/run", func(c *gin.Context) {
		res, err := workers.RunSweeperNow()
		if err != nil {
			c.JSON(500, gin.H{"success": false, "error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"success": true, "data": res})
	})
}
