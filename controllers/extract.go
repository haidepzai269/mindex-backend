package controllers

import (
	"encoding/json"
	"mindex-backend/utils"

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
