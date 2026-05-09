package controllers

import (
	"math"
	"mindex-backend/config"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// CreateStudyPlan — POST /study/plans
func CreateStudyPlan(c *gin.Context) {
	userID := c.GetString("user_id")
	var req struct {
		Name        string   `json:"name" binding:"required"`
		ExamDate    string   `json:"exam_date" binding:"required"` // "2026-06-15"
		DocumentIDs []string `json:"doc_ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "message": "Thiếu thông tin"})
		return
	}

	examDate, err := time.Parse("2006-01-02", req.ExamDate)
	if err != nil {
		c.JSON(400, gin.H{"success": false, "message": "Định dạng ngày thi không hợp lệ (YYYY-MM-DD)"})
		return
	}
	if examDate.Before(time.Now()) {
		c.JSON(400, gin.H{"success": false, "message": "Ngày thi phải ở tương lai"})
		return
	}

	daysLeft := int(math.Ceil(time.Until(examDate).Hours() / 24))

	// Tính tổng trang ước tính từ chunk_count
	totalPages := 0
	for _, docID := range req.DocumentIDs {
		var chunks int
		config.DB.QueryRow(config.Ctx,
			`SELECT COUNT(*) FROM document_chunks WHERE document_id=$1`, docID).Scan(&chunks)
		totalPages += int(math.Ceil(float64(chunks) / 4))
	}

	pagesPerDay := 0
	if daysLeft > 0 && totalPages > 0 {
		pagesPerDay = int(math.Ceil(float64(totalPages) / float64(daysLeft)))
	}

	planID := uuid.New().String()
	_, err = config.DB.Exec(config.Ctx,
		`INSERT INTO study_plans (id, user_id, name, exam_date, doc_ids, total_pages, pages_per_day)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		planID, userID, req.Name, examDate, req.DocumentIDs, totalPages, pagesPerDay)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Không thể tạo kế hoạch"})
		return
	}

	c.JSON(201, gin.H{"success": true, "data": gin.H{
		"id":            planID,
		"name":          req.Name,
		"exam_date":     req.ExamDate,
		"days_left":     daysLeft,
		"total_pages":   totalPages,
		"pages_per_day": pagesPerDay,
	}})
}

// GetStudyPlans — GET /study/plans
func GetStudyPlans(c *gin.Context) {
	userID := c.GetString("user_id")

	rows, err := config.DB.Query(config.Ctx, `
		SELECT id, name, exam_date, doc_ids, total_pages, pages_per_day, created_at
		FROM study_plans WHERE user_id=$1
		ORDER BY exam_date ASC`, userID)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Query error"})
		return
	}
	defer rows.Close()

	type Plan struct {
		ID          string    `json:"id"`
		Name        string    `json:"name"`
		ExamDate    time.Time `json:"exam_date"`
		DocIDs      []string  `json:"doc_ids"`
		TotalPages  int       `json:"total_pages"`
		PagesPerDay int       `json:"pages_per_day"`
		DaysLeft    int       `json:"days_left"`
		CreatedAt   time.Time `json:"created_at"`
	}
	var plans []Plan
	for rows.Next() {
		var p Plan
		rows.Scan(&p.ID, &p.Name, &p.ExamDate, &p.DocIDs, &p.TotalPages, &p.PagesPerDay, &p.CreatedAt)
		p.DaysLeft = int(math.Ceil(time.Until(p.ExamDate).Hours() / 24))
		if p.DaysLeft < 0 {
			p.DaysLeft = 0
		}
		plans = append(plans, p)
	}
	if plans == nil {
		plans = []Plan{}
	}
	c.JSON(200, gin.H{"success": true, "data": plans})
}

// DeleteStudyPlan — DELETE /study/plans/:id
func DeleteStudyPlan(c *gin.Context) {
	userID := c.GetString("user_id")
	planID := c.Param("id")
	res, err := config.DB.Exec(config.Ctx,
		`DELETE FROM study_plans WHERE id=$1 AND user_id=$2`, planID, userID)
	if err != nil || res.RowsAffected() == 0 {
		c.JSON(404, gin.H{"success": false, "message": "Không tìm thấy kế hoạch"})
		return
	}
	c.JSON(200, gin.H{"success": true, "message": "Đã xóa kế hoạch"})
}
