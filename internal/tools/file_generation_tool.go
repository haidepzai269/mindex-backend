package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jung-kurt/gofpdf"
	"github.com/xuri/excelize/v2"

	"mindex-backend/utils"
)

// FileGenerationTool exports AI-produced structured content to a downloadable
// PDF or XLSX file (US7), stored via the existing Cloudinary raw-upload
// integration (no new storage backend, per spec Assumption).
//
// Known limitation: Cloudinary's simple raw upload has no built-in TTL/auto-
// delete here (UploadRawToCloudinary doesn't support one), so generated
// files persist until manually cleaned up - the spec's "TTL để tự xóa"
// assumption is not yet enforced. DOCX is not implemented (no DOCX-capable
// Go library was already in use; only PDF/XLSX ship in this pass).
type FileGenerationTool struct{}

func NewFileGenerationTool() *FileGenerationTool { return &FileGenerationTool{} }

func (t *FileGenerationTool) Name() string { return "file_generation" }
func (t *FileGenerationTool) Description() string {
	return "Xuất nội dung (văn bản hoặc bảng dữ liệu) ra file PDF hoặc XLSX để user download. Dùng khi user yêu cầu xuất file/tài liệu."
}
func (t *FileGenerationTool) Category() ToolCategory { return CategoryContentGeneration }
func (t *FileGenerationTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"format": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"pdf", "xlsx"},
				"description": "Định dạng file cần xuất",
			},
			"title": map[string]interface{}{
				"type":        "string",
				"description": "Tiêu đề nội dung",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "Nội dung văn bản, dùng xuống dòng (\\n) để ngăn đoạn (chỉ dùng với format=pdf)",
			},
			"table": map[string]interface{}{
				"type":        "array",
				"description": "Dữ liệu dạng bảng: mảng các dòng, mỗi dòng là mảng chuỗi (chỉ dùng với format=xlsx)",
				"items": map[string]interface{}{
					"type":  "array",
					"items": map[string]interface{}{"type": "string"},
				},
			},
		},
		"required": []string{"format"},
	}
}
func (t *FileGenerationTool) Timeout() time.Duration     { return 15 * time.Second }
func (t *FileGenerationTool) TierLimit() map[string]int  { return nil }
func (t *FileGenerationTool) RequiresConfirmation() bool { return false }

const (
	fileGenMaxContentChars = 50000
	fileGenMaxTableRows    = 5000
)

type fileGenerationInput struct {
	Format  string     `json:"format"`
	Title   string     `json:"title"`
	Content string     `json:"content"`
	Table   [][]string `json:"table"`
}

func (t *FileGenerationTool) Execute(ctx context.Context, execCtx ToolExecutionContext, input json.RawMessage) (ToolResult, error) {
	var in fileGenerationInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{}, fmt.Errorf("file_generation: invalid input: %v", err)
	}

	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = "Mindex Export"
	}

	switch in.Format {
	case "pdf":
		return t.generatePDF(title, in.Content)
	case "xlsx":
		return t.generateXLSX(title, in.Table)
	default:
		return ToolResult{}, fmt.Errorf("file_generation: định dạng %q không được hỗ trợ (hiện chỉ hỗ trợ pdf, xlsx)", in.Format)
	}
}

func (t *FileGenerationTool) generatePDF(title, content string) (ToolResult, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return ToolResult{}, fmt.Errorf("file_generation: content không được rỗng khi xuất PDF")
	}
	if len(content) > fileGenMaxContentChars {
		return ToolResult{}, fmt.Errorf("file_generation: nội dung quá lớn (%d ký tự, tối đa %d) để xuất PDF", len(content), fileGenMaxContentChars)
	}

	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.AddPage()
	pdf.SetFont("Arial", "B", 16)
	pdf.MultiCell(0, 10, title, "", "L", false)
	pdf.Ln(4)
	pdf.SetFont("Arial", "", 12)
	for _, paragraph := range strings.Split(content, "\n") {
		pdf.MultiCell(0, 7, paragraph, "", "L", false)
	}

	tmpFile, err := os.CreateTemp("", "mindex-export-*.pdf")
	if err != nil {
		return ToolResult{}, fmt.Errorf("file_generation: lỗi tạo file tạm: %v", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	if err := pdf.OutputFileAndClose(tmpPath); err != nil {
		return ToolResult{}, fmt.Errorf("file_generation: lỗi tạo PDF: %v", err)
	}

	return t.upload(tmpPath, "pdf")
}

func (t *FileGenerationTool) generateXLSX(title string, table [][]string) (ToolResult, error) {
	if len(table) == 0 {
		return ToolResult{}, fmt.Errorf("file_generation: table không được rỗng khi xuất XLSX")
	}
	if len(table) > fileGenMaxTableRows {
		return ToolResult{}, fmt.Errorf("file_generation: bảng quá lớn (%d dòng, tối đa %d) để xuất XLSX", len(table), fileGenMaxTableRows)
	}

	f := excelize.NewFile()
	defer f.Close()
	sheet := f.GetSheetName(0)
	if title != "" {
		safeName := title
		if len(safeName) > 31 { // Excel sheet name hard limit
			safeName = safeName[:31]
		}
		if err := f.SetSheetName(sheet, safeName); err == nil {
			sheet = safeName
		}
	}

	for rowIdx, row := range table {
		for colIdx, cell := range row {
			cellRef, err := excelize.CoordinatesToCellName(colIdx+1, rowIdx+1)
			if err != nil {
				continue
			}
			_ = f.SetCellValue(sheet, cellRef, cell)
		}
	}

	tmpFile, err := os.CreateTemp("", "mindex-export-*.xlsx")
	if err != nil {
		return ToolResult{}, fmt.Errorf("file_generation: lỗi tạo file tạm: %v", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	if err := f.SaveAs(tmpPath); err != nil {
		return ToolResult{}, fmt.Errorf("file_generation: lỗi tạo XLSX: %v", err)
	}

	return t.upload(tmpPath, "xlsx")
}

func (t *FileGenerationTool) upload(tmpPath, ext string) (ToolResult, error) {
	publicID := fmt.Sprintf("mindex_tool_files/%s.%s", uuid.New().String(), ext)
	result, err := utils.UploadRawToCloudinary(tmpPath, publicID)
	if err != nil {
		return ToolResult{}, fmt.Errorf("file_generation: lỗi upload: %v", err)
	}
	return ToolResult{
		TextOutput: fmt.Sprintf("Đã tạo file %s, tải về tại: %s", strings.ToUpper(ext), result.SecureURL),
		FileURL:    result.SecureURL,
	}, nil
}
