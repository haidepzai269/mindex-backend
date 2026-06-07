package routes

import (
	"time"

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
	doc.GET("/documents/:id/content", controllers.GetDocumentContent)
	doc.PATCH("/documents/:id/pin", controllers.TogglePinDocument)
	doc.PATCH("/documents/:id/persona", controllers.UpdateDocumentPersona)
	doc.DELETE("/documents/:id", controllers.DeleteDocument)

	// Tóm tắt & Trích xuất — tất cả đều gọi AI, giới hạn per-user
	aiRL := middleware.RateLimit("ai_extract", 20, time.Minute)  // 20 req/phút cho extract đơn
	heavyRL := middleware.RateLimit("ai_heavy", 10, time.Minute) // 10 req/phút cho heavy ops

	doc.POST("/summary/quick", aiRL, controllers.QuickSummary)
	doc.POST("/summary/detailed", heavyRL, controllers.DetailedSummary) // Map-Reduce, đắt nhất
	doc.GET("/summary/cache/:id", controllers.GetCachedSummary)
	doc.POST("/extract/keywords", aiRL, controllers.ExtractKeywords)
	doc.POST("/extract/keywords/stream", aiRL, controllers.ExtractKeywordsStream)
	doc.POST("/extract/timeline", aiRL, controllers.ExtractTimeline)
	doc.POST("/extract/timeline/stream", aiRL, controllers.ExtractTimelineStream)
	doc.POST("/extract/formulas", aiRL, controllers.ExtractFormulas)
	doc.POST("/extract/formulas/stream", aiRL, controllers.ExtractFormulasStream)
	doc.POST("/extract/compare", heavyRL, controllers.ExtractCompare) // So sánh nhiều doc
	doc.POST("/extract/compare/stream", heavyRL, controllers.ExtractCompareStream)
	doc.POST("/extract/mindmap", aiRL, controllers.ExtractMindMap)
	doc.POST("/extract/mindmap/stream", aiRL, controllers.ExtractMindMapStream)

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
