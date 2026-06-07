package controllers

import (
	"encoding/json"
	"fmt"
	"mindex-backend/config"
	"mindex-backend/utils"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type extractStreamEmitter func(event string, payload gin.H)

type extractStreamError struct {
	status  int
	message string
}

func (e extractStreamError) Error() string {
	return e.message
}

func newExtractStreamError(status int, message string) error {
	return extractStreamError{status: status, message: message}
}

func setupExtractStream(c *gin.Context) (extractStreamEmitter, bool) {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Streaming unsupported"})
		return nil, false
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	emit := func(event string, payload gin.H) {
		data, _ := json.Marshal(payload)
		fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event, data)
		flusher.Flush()
	}

	return emit, true
}

func streamExtractResponse(c *gin.Context, run func(extractStreamEmitter) (gin.H, error)) {
	emit, ok := setupExtractStream(c)
	if !ok {
		return
	}

	emit("status", gin.H{"step": "received", "message": "Đang phân tích tài liệu..."})

	result, err := run(emit)
	if err != nil {
		status := http.StatusInternalServerError
		if streamErr, ok := err.(extractStreamError); ok {
			status = streamErr.status
		}
		emit("error", gin.H{"status": status, "message": err.Error()})
		emit("done", gin.H{"status": "error"})
		return
	}

	emit("result", result)
	emit("done", gin.H{"status": "completed"})
}

func emitExtractStatus(emit extractStreamEmitter, step string, message string) {
	if emit != nil {
		emit("status", gin.H{"step": step, "message": message})
	}
}

func emitExtractInsight(emit extractStreamEmitter, text string) {
	if emit != nil {
		emit("insight", gin.H{"text": text})
	}
}

func parseExtractJSON(raw string) map[string]interface{} {
	parsed := map[string]interface{}{}
	_ = json.Unmarshal([]byte(utils.CleanJSONString(raw)), &parsed)
	return parsed
}

func readExtractDocumentText(documentID string, limit int, emit extractStreamEmitter) string {
	emitExtractStatus(emit, "read_document", "Đang đọc nội dung tài liệu...")
	content := getAllDocumentText(documentID, limit)
	if content == "" {
		emitExtractInsight(emit, "Tài liệu không có nội dung văn bản để phân tích.")
		return content
	}
	emitExtractInsight(emit, fmt.Sprintf("Đã đọc %d ký tự từ tài liệu.", len(content)))
	return content
}

func ExtractKeywordsStream(c *gin.Context) {
	var req SummaryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Du lieu khong hop le"})
		return
	}

	streamExtractResponse(c, func(emit extractStreamEmitter) (gin.H, error) {
		content := readExtractDocumentText(req.DocumentID, 20, emit)
		emitExtractStatus(emit, "identify_terms", "AI đang nhận diện thuật ngữ và khái niệm cốt lõi...")
		emitExtractInsight(emit, "Đang gom nhóm từ khóa, định nghĩa và dữ kiện quan trọng.")

		sysPrompt := `Ban la cong cu phan tich tai lieu hoc thuat. Trich xuat thong tin tu tai lieu va tra ve CHINH XAC theo dinh dang JSON sau, khong co text them:
{
 "keywords": [ {"term": "...", "frequency": 5, "importance": "high|medium|low", "definition": "..."} ],
 "core_concepts": [ {"name": "...", "explanation": "giai thich", "example": "vi du", "related_to": ["khai niem"]} ],
 "formulas": [ {"name": "...", "formula": "...", "variables": "giai thich bien", "usage": "khi nao dung"} ],
 "key_facts": [ {"fact": "...", "source_page": 0, "category": "definition|theorem|rule"} ]
}`

		messages := []utils.ChatMessage{
			{Role: "system", Content: sysPrompt},
			{Role: "user", Content: "Trich xuat JSON tu tai lieu sau:\n" + content},
		}

		res, _, err := utils.AI.ChatNonStream(utils.ServiceSummary, messages)
		if err != nil {
			return nil, newExtractStreamError(http.StatusInternalServerError, "AI_SERVICE_DOWN")
		}

		emitExtractStatus(emit, "normalize_result", "Đang chuẩn hóa kết quả trích xuất...")
		return gin.H{"data": parseExtractJSON(res)}, nil
	})
}

func ExtractFormulasStream(c *gin.Context) {
	var req SummaryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Du lieu khong hop le"})
		return
	}

	streamExtractResponse(c, func(emit extractStreamEmitter) (gin.H, error) {
		content := readExtractDocumentText(req.DocumentID, 15, emit)
		emitExtractStatus(emit, "detect_formulas", "AI đang quét công thức, định lý và ký hiệu...")
		emitExtractInsight(emit, "Đang phân loại công thức theo lĩnh vực và cách áp dụng.")

		sysPrompt := `Trich xuat TAT CA cong thuc, phuong trinh, dinh ly, hang so tu tai lieu. Tra ve JSON duy nhat (khong markdown):
{
 "formulas": [
   {
     "name": "Ten cong thuc/dinh ly",
     "formula": "Ky hieu toan hoc hoac code",
     "variables": "Giai thich cac bien/tham so",
     "usage": "Khi nao ap dung cong thuc nay",
     "category": "algebra|calculus|physics|chemistry|programming|statistics|other",
     "difficulty": "basic|intermediate|advanced"
   }
 ]
}`

		messages := []utils.ChatMessage{
			{Role: "system", Content: sysPrompt},
			{Role: "user", Content: "Noi dung tai lieu:\n" + content},
		}

		res, _, err := utils.AI.ChatNonStream(utils.ServiceSummary, messages)
		if err != nil {
			return nil, newExtractStreamError(http.StatusInternalServerError, "AI_SERVICE_DOWN")
		}

		emitExtractStatus(emit, "normalize_result", "Đang chuẩn hóa danh sách công thức...")
		return gin.H{"data": parseExtractJSON(res)}, nil
	})
}

func ExtractTimelineStream(c *gin.Context) {
	var req SummaryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Du lieu khong hop le"})
		return
	}

	streamExtractResponse(c, func(emit extractStreamEmitter) (gin.H, error) {
		content := readExtractDocumentText(req.DocumentID, 20, emit)
		emitExtractStatus(emit, "order_events", "AI đang tìm mốc thời gian và quy trình có thứ tự...")
		emitExtractInsight(emit, "Đang sắp xếp sự kiện theo mạch logic của tài liệu.")

		sysPrompt := `Trich xuat TAT CA su kien, moc thoi gian, quy trinh co thu tu tu tai lieu. Tra ve JSON duy nhat (khong markdown):
{
 "timeline": [ {"date_or_step": "...", "event": "mo ta", "significance": "quan trong vi", "page_ref": 1} ],
 "processes": [ {"name": "...", "steps": ["buoc 1", "buoc 2"]} ]
}`

		messages := []utils.ChatMessage{
			{Role: "system", Content: sysPrompt},
			{Role: "user", Content: "Noi dung:\n" + content},
		}

		res, _, err := utils.AI.ChatNonStream(utils.ServiceSummary, messages)
		if err != nil {
			return nil, newExtractStreamError(http.StatusInternalServerError, "AI_SERVICE_DOWN")
		}

		emitExtractStatus(emit, "normalize_result", "Đang tạo dòng thời gian...")
		return gin.H{"data": parseExtractJSON(res)}, nil
	})
}

func ExtractMindMapStream(c *gin.Context) {
	var req SummaryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Du lieu khong hop le"})
		return
	}

	streamExtractResponse(c, func(emit extractStreamEmitter) (gin.H, error) {
		content := readExtractDocumentText(req.DocumentID, 15, emit)
		emitExtractStatus(emit, "structure_topics", "AI đang tách chủ đề trung tâm và các nhánh phụ...")
		emitExtractInsight(emit, "Đang xây dựng cấu trúc mind map từ các cụm kiến thức.")

		sysPrompt := `Tao cau truc mind map tu tai lieu. Tra ve JSON duy nhat (khong markdown):
{
 "root": {
   "id": "root",
   "label": "Chu de chinh cua tai lieu",
   "children": [
     {
       "id": "node_1",
       "label": "Nhanh chinh 1",
       "color": "#3b82f6",
       "children": [
         {"id": "node_1_1", "label": "Chi tiet 1.1", "children": []},
         {"id": "node_1_2", "label": "Chi tiet 1.2", "children": []}
       ]
     }
   ]
 }
}
Mau sac cho cac nhanh chinh: #3b82f6, #10b981, #f59e0b, #ef4444, #8b5cf6, #06b6d4`

		messages := []utils.ChatMessage{
			{Role: "system", Content: sysPrompt},
			{Role: "user", Content: "Noi dung tai lieu:\n" + content},
		}

		res, _, err := utils.AI.ChatNonStream(utils.ServiceSummary, messages)
		if err != nil {
			return nil, newExtractStreamError(http.StatusInternalServerError, "AI_SERVICE_DOWN")
		}

		emitExtractStatus(emit, "normalize_result", "Đang hoàn thiện cấu trúc mind map...")
		return gin.H{"data": parseExtractJSON(res)}, nil
	})
}

func ExtractCompareStream(c *gin.Context) {
	userID := c.GetString("user_id")
	var req struct {
		DocumentIDs []string `json:"document_ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.DocumentIDs) < 2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cần ít nhất 2 document_ids"})
		return
	}
	if len(req.DocumentIDs) > 4 {
		req.DocumentIDs = req.DocumentIDs[:4]
	}

	streamExtractResponse(c, func(emit extractStreamEmitter) (gin.H, error) {
		type docInfo struct {
			ID      string
			Title   string
			Content string
		}

		emitExtractStatus(emit, "load_documents", "Đang tải nội dung các tài liệu đã chọn...")

		var docs []docInfo
		for _, docID := range req.DocumentIDs {
			var title string
			err := config.DB.QueryRow(config.Ctx,
				`SELECT d.title FROM documents d
				 JOIN document_references dr ON dr.document_id = d.id
				 WHERE d.id = $1 AND dr.user_id = $2`, docID, userID).Scan(&title)
			if err != nil {
				return nil, newExtractStreamError(http.StatusForbidden, fmt.Sprintf("Tài liệu %s không tồn tại hoặc không có quyền", docID))
			}
			content := getAllDocumentText(docID, 8)
			docs = append(docs, docInfo{ID: docID, Title: title, Content: content})
		}

		emitExtractInsight(emit, fmt.Sprintf("Đã tải %d tài liệu để so sánh.", len(docs)))
		emitExtractStatus(emit, "compare_themes", "AI đang tìm điểm chung và điểm khác biệt...")

		var sb strings.Builder
		for i, d := range docs {
			sb.WriteString(fmt.Sprintf("\n=== TAI LIEU %d: %s ===\n%s\n", i+1, d.Title, d.Content))
		}

		titles := make([]string, len(docs))
		for i, d := range docs {
			titleJSON, _ := json.Marshal(d.Title)
			titles[i] = string(titleJSON)
		}

		sysPrompt := fmt.Sprintf(`So sanh %d tai lieu sau day. Tra ve JSON duy nhat (khong markdown):
{
 "summary": "Tom tat tong quan ve cac tai lieu",
 "common_themes": ["chu de chung 1", "chu de chung 2"],
 "differences": [
   {
     "aspect": "Khia canh so sanh",
     "values": %s
   }
 ],
 "unique_to": [
   {"document": "Ten tai lieu", "unique_points": ["diem noi bat chi co o tai lieu nay"]}
 ],
 "recommendation": "Nen doc theo thu tu nao va tai sao"
}`, len(docs), "["+strings.Join(titles, ", ")+"]")

		messages := []utils.ChatMessage{
			{Role: "system", Content: sysPrompt},
			{Role: "user", Content: sb.String()},
		}

		res, _, err := utils.AI.ChatNonStream(utils.ServiceSummary, messages)
		if err != nil {
			return nil, newExtractStreamError(http.StatusInternalServerError, "AI_SERVICE_DOWN")
		}

		docMeta := make([]gin.H, len(docs))
		for i, d := range docs {
			docMeta[i] = gin.H{"id": d.ID, "title": d.Title}
		}

		emitExtractStatus(emit, "normalize_result", "Đang tổng hợp kết luận so sánh...")
		return gin.H{"data": parseExtractJSON(res), "documents": docMeta}, nil
	})
}
