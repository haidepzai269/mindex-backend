package controllers

import (
	"encoding/json"
	"fmt"
	"mindex-backend/config"
	"mindex-backend/utils"
	"strings"

	"github.com/gin-gonic/gin"
)

func ExtractKeywords(c *gin.Context) {
	var req SummaryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "Dữ liệu không hợp lệ"})
		return
	}

	content := getAllDocumentText(req.DocumentID, 20)

	sysPrompt := `Bạn là công cụ phân tích tài liệu học thuật. Trích xuất thông tin từ tài liệu và trả về CHÍNH XÁC theo định dạng JSON sau, không có text thêm:
{
 "keywords": [ {"term": "...", "frequency": 5, "importance": "high|medium|low", "definition": "..."} ],
 "core_concepts": [ {"name": "...", "explanation": "giải thích", "example": "ví dụ", "related_to": ["khái niệm"]} ],
 "formulas": [ {"name": "...", "formula": "...", "variables": "giải thích biến", "usage": "khi nào dùng"} ],
 "key_facts": [ {"fact": "...", "source_page": 0, "category": "definition|theorem|rule"} ]
}`

	messages := []utils.ChatMessage{
		{Role: "system", Content: sysPrompt},
		{Role: "user", Content: "Trích xuất JSON từ tài liệu sau:\n" + content},
	}

	res, _, err := utils.AI.ChatNonStream(utils.ServiceSummary, messages)
	if err != nil {
		c.JSON(500, gin.H{"error": "AI_SERVICE_DOWN"})
		return
	}

	// Groq sometimes wraps JSON in markdown blocks even if instructed not to. Clean it up.
	res = utils.CleanJSONString(res)

	var parsed map[string]interface{}
	json.Unmarshal([]byte(res), &parsed)

	c.JSON(200, gin.H{
		"data": parsed,
	})
}

// ExtractFormulas trích xuất riêng công thức từ tài liệu
// POST /extract/formulas
func ExtractFormulas(c *gin.Context) {
	var req SummaryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "Dữ liệu không hợp lệ"})
		return
	}

	content := getAllDocumentText(req.DocumentID, 15)
	sysPrompt := `Trích xuất TẤT CẢ công thức, phương trình, định lý, hằng số từ tài liệu. Trả về JSON duy nhất (không markdown):
{
 "formulas": [
   {
     "name": "Tên công thức/định lý",
     "formula": "Ký hiệu toán học hoặc code",
     "variables": "Giải thích các biến/tham số",
     "usage": "Khi nào áp dụng công thức này",
     "category": "algebra|calculus|physics|chemistry|programming|statistics|other",
     "difficulty": "basic|intermediate|advanced"
   }
 ]
}`

	messages := []utils.ChatMessage{
		{Role: "system", Content: sysPrompt},
		{Role: "user", Content: "Nội dung tài liệu:\n" + content},
	}

	res, _, err := utils.AI.ChatNonStream(utils.ServiceSummary, messages)
	if err != nil {
		c.JSON(500, gin.H{"error": "AI_SERVICE_DOWN"})
		return
	}

	var parsed map[string]interface{}
	json.Unmarshal([]byte(utils.CleanJSONString(res)), &parsed)
	c.JSON(200, gin.H{"data": parsed})
}

// ExtractCompare so sánh nhiều tài liệu với nhau
// POST /extract/compare
func ExtractCompare(c *gin.Context) {
	userID := c.GetString("user_id")
	var req struct {
		DocumentIDs []string `json:"document_ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.DocumentIDs) < 2 {
		c.JSON(400, gin.H{"error": "Cần ít nhất 2 document_ids"})
		return
	}
	if len(req.DocumentIDs) > 4 {
		req.DocumentIDs = req.DocumentIDs[:4]
	}

	// Kiểm tra quyền và lấy nội dung từng tài liệu
	type DocInfo struct {
		ID      string
		Title   string
		Content string
	}
	var docs []DocInfo
	for _, docID := range req.DocumentIDs {
		var title string
		err := config.DB.QueryRow(config.Ctx,
			`SELECT d.title FROM documents d
			 JOIN document_references dr ON dr.document_id = d.id
			 WHERE d.id = $1 AND dr.user_id = $2`, docID, userID).Scan(&title)
		if err != nil {
			c.JSON(403, gin.H{"error": fmt.Sprintf("Tài liệu %s không tồn tại hoặc không có quyền", docID)})
			return
		}
		content := getAllDocumentText(docID, 8)
		docs = append(docs, DocInfo{ID: docID, Title: title, Content: content})
	}

	// Chuẩn bị prompt
	var sb strings.Builder
	for i, d := range docs {
		sb.WriteString(fmt.Sprintf("\n=== TÀI LIỆU %d: %s ===\n%s\n", i+1, d.Title, d.Content))
	}

	titles := make([]string, len(docs))
	for i, d := range docs {
		titles[i] = d.Title
	}

	sysPrompt := fmt.Sprintf(`So sánh %d tài liệu sau đây. Trả về JSON duy nhất (không markdown):
{
 "summary": "Tóm tắt tổng quan về các tài liệu",
 "common_themes": ["chủ đề chung 1", "chủ đề chung 2"],
 "differences": [
   {
     "aspect": "Khía cạnh so sánh",
     "values": %s
   }
 ],
 "unique_to": [
   {"document": "Tên tài liệu", "unique_points": ["điểm nổi bật chỉ có ở tài liệu này"]}
 ],
 "recommendation": "Nên đọc theo thứ tự nào và tại sao"
}`, len(docs), func() string {
		parts := make([]string, len(titles))
		for i, t := range titles {
			parts[i] = fmt.Sprintf(`"%s"`, t)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}())

	messages := []utils.ChatMessage{
		{Role: "system", Content: sysPrompt},
		{Role: "user", Content: sb.String()},
	}

	res, _, err := utils.AI.ChatNonStream(utils.ServiceSummary, messages)
	if err != nil {
		c.JSON(500, gin.H{"error": "AI_SERVICE_DOWN"})
		return
	}

	var parsed map[string]interface{}
	json.Unmarshal([]byte(utils.CleanJSONString(res)), &parsed)

	// Đính kèm thông tin meta của các tài liệu
	docMeta := make([]gin.H, len(docs))
	for i, d := range docs {
		docMeta[i] = gin.H{"id": d.ID, "title": d.Title}
	}

	c.JSON(200, gin.H{"data": parsed, "documents": docMeta})
}

// ExtractMindMap tạo mind map từ tài liệu
// POST /extract/mindmap
func ExtractMindMap(c *gin.Context) {
	var req SummaryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "Dữ liệu không hợp lệ"})
		return
	}

	content := getAllDocumentText(req.DocumentID, 15)
	sysPrompt := `Tạo cấu trúc mind map từ tài liệu. Trả về JSON duy nhất (không markdown):
{
 "root": {
   "id": "root",
   "label": "Chủ đề chính của tài liệu",
   "children": [
     {
       "id": "node_1",
       "label": "Nhánh chính 1",
       "color": "#3b82f6",
       "children": [
         {"id": "node_1_1", "label": "Chi tiết 1.1", "children": []},
         {"id": "node_1_2", "label": "Chi tiết 1.2", "children": []}
       ]
     }
   ]
 }
}
Màu sắc cho các nhánh chính: #3b82f6, #10b981, #f59e0b, #ef4444, #8b5cf6, #06b6d4`

	messages := []utils.ChatMessage{
		{Role: "system", Content: sysPrompt},
		{Role: "user", Content: "Nội dung tài liệu:\n" + content},
	}

	res, _, err := utils.AI.ChatNonStream(utils.ServiceSummary, messages)
	if err != nil {
		c.JSON(500, gin.H{"error": "AI_SERVICE_DOWN"})
		return
	}

	var parsed map[string]interface{}
	json.Unmarshal([]byte(utils.CleanJSONString(res)), &parsed)
	c.JSON(200, gin.H{"data": parsed})
}

func ExtractTimeline(c *gin.Context) {
	var req SummaryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "Dữ liệu không hợp lệ"})
		return
	}

	content := getAllDocumentText(req.DocumentID, 20)
	sysPrompt := `Trích xuất TẤT CẢ sự kiện, mốc thời gian, quy trình có thứ tự từ tài liệu. Trả về JSON duy nhất (không markdown):
{
 "timeline": [ {"date_or_step": "...", "event": "mô tả", "significance": "quan trọng vì", "page_ref": 1} ],
 "processes": [ {"name": "...", "steps": ["bước 1", "bước 2"]} ]
}`

	messages := []utils.ChatMessage{
		{Role: "system", Content: sysPrompt},
		{Role: "user", Content: "Nội dung:\n" + content},
	}

	res, _, _ := utils.AI.ChatNonStream(utils.ServiceSummary, messages)
	var parsed map[string]interface{}
	json.Unmarshal([]byte(utils.CleanJSONString(res)), &parsed)

	c.JSON(200, gin.H{"data": parsed})
}
