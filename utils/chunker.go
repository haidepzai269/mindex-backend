package utils

import (
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"
)

// Block struct matches the output from extractor.py
type Block struct {
	Type    string `json:"type"` // heading1/2/3, paragraph, table, code, list_item, empty
	Content string `json:"content"`
	Page    int    `json:"page"`
	Level   int    `json:"level"`
}

type Chunk struct {
	Content          string // Nội dung dùng cho Embedding (Sạch)
	RetrievalContent string // Nội dung trả về cho LLM (Có Breadcrumb, Overlap)
	Heading          string
	TokenCount       int
	PageStart        int
	ChunkIndex       int
}

const (
	TargetTokens        = 500
	OverlapSentences    = 2
	MinHeadingsRequired = 2
)

// ExtractAndChunk process the document into chunks.
// Replaces SplitIntoChunks for documents.
func ExtractAndChunk(filePath string, cleanTextFunc func(string) string) ([]Chunk, error) {
	blocks, err := extractBlocks(filePath)
	if err != nil {
		return nil, fmt.Errorf("extraction error: %v", err)
	}

	if len(blocks) == 0 || blocks[0].Type == "empty" {
		return nil, fmt.Errorf("no text found in document (possible image-only scan)")
	}

	// Clean all block contents
	for i := range blocks {
		blocks[i].Content = cleanTextFunc(blocks[i].Content)
	}

	if needsLLMFallback(blocks) {
		log.Printf("⚠️ Low structure document. Fallback to Groq structural enhancement.")
		return chunkWithGroqFallback(blocks), nil
	}

	return buildChunks(blocks), nil
}

func extractBlocks(filePath string) ([]Block, error) {
	commands := []string{"py", "python", "python3"}
	var out []byte
	var err error
	var success bool

	var pythonErrorResponse struct {
		Error string `json:"error"`
	}

	for _, pyCmd := range commands {
		cmd := exec.Command(pyCmd, "extractor.py", filePath)
		out, err = cmd.CombinedOutput()
		if err == nil {
			success = true
			break
		}
		if _, isExitError := err.(*exec.ExitError); isExitError {
			// Lệnh python được tìm thấy nhưng script chạy lỗi, không cần thử lệnh python khác
			break
		}
	}

	if !success {
		if strings.Contains(string(out), "\"error\":") {
			json.Unmarshal(out, &pythonErrorResponse)
			if pythonErrorResponse.Error != "" {
				return nil, fmt.Errorf("python script error: %s", pythonErrorResponse.Error)
			}
		}
		return nil, fmt.Errorf("failed to run extractor script: %v. Output: %s", err, string(out))
	}

	var blocks []Block

	err = json.Unmarshal(out, &blocks)
	if err != nil {
		return nil, fmt.Errorf("malformed JSON from extractor: %v. Output snippet: %s", err, string(out[:min(len(out), 200)]))
	}

	return blocks, nil
}

func buildChunks(blocks []Block) []Chunk {
	var chunks []Chunk
	var current strings.Builder
	var currentOverlap string
	currentTokens := 0
	
	// Breadcrumb logic variables
	h1, h2, h3 := "", "", ""
	currentHeading := ""
	
	currentPage := 0
	chunkIdx := 0

	updateBreadcrumb := func(typ, content string) {
		short := shortenBreadcrumb(content)
		switch typ {
		case "heading1":
			h1 = short
			h2, h3 = "", ""
		case "heading2":
			h2 = short
			h3 = ""
		case "heading3":
			h3 = short
		}
		
		var parts []string
		if h1 != "" { parts = append(parts, h1) }
		if h2 != "" { parts = append(parts, h2) }
		if h3 != "" { parts = append(parts, h3) }
		
		if len(parts) > 0 {
			currentHeading = fmt.Sprintf("[%s]", strings.Join(parts, " > "))
		} else {
			currentHeading = ""
		}
	}

	flush := func() {
		if current.Len() == 0 {
			return
		}
		cleanContent := strings.TrimSpace(current.String())
		if cleanContent == "" {
			return
		}
		
		retrievalContent := buildRetrievalContent(currentHeading, currentOverlap, cleanContent)

		chunks = append(chunks, Chunk{
			Content:          cleanContent,
			RetrievalContent: retrievalContent,
			Heading:          currentHeading,
			TokenCount:       estimateTokens(cleanContent), // Embedding dựa trên nội dung sạch
			PageStart:        currentPage,
			ChunkIndex:       chunkIdx,
		})
		chunkIdx++
		current.Reset()
		currentTokens = 0
		currentOverlap = "" // Reset overlap after flush
	}

	for i, block := range blocks {
		if block.Content == "" { continue }
		tokens := estimateTokens(block.Content)

		switch block.Type {
		case "heading1":
			flush()
			updateBreadcrumb("heading1", block.Content)
			currentPage = block.Page
			current.WriteString("# " + block.Content + "\n\n")
			currentTokens += tokens

		case "heading2":
			if currentTokens >= TargetTokens/2 {
				flush()
				currentPage = block.Page
			}
			updateBreadcrumb("heading2", block.Content)
			current.WriteString("## " + block.Content + "\n\n")
			currentTokens += tokens

		case "heading3":
			if currentTokens >= TargetTokens {
				flush()
				currentPage = block.Page
			}
			updateBreadcrumb("heading3", block.Content)
			current.WriteString("### " + block.Content + "\n\n")
			currentTokens += tokens

		case "table", "code":
			if currentPage == 0 { currentPage = block.Page }
			current.WriteString(block.Content + "\n\n")
			currentTokens += tokens

		case "list_item":
			if currentTokens+tokens > TargetTokens {
				flush()
				currentPage = block.Page
			}
			current.WriteString("• " + block.Content + "\n")
			currentTokens += tokens

		default: 
			if currentPage == 0 { currentPage = block.Page }
			if currentTokens+tokens > TargetTokens {
				flush()
				currentPage = block.Page
				if len(chunks) > 0 {
					var prevContent string
					if i > 0 {
						prevContent = blocks[i-1].Content
					}
					if needsOverlap(block.Type, prevContent) {
						overlap := getLastSentences(chunks[len(chunks)-1].Content, OverlapSentences)
						if overlap != "" {
							currentOverlap = "..." + overlap // Dùng cho RetrievalContent
						}
					}
				}
			}
			current.WriteString(block.Content + "\n\n")
			currentTokens += tokens
		}
	}

	flush()
	return chunks
}

func needsLLMFallback(blocks []Block) bool {
	headings := 0
	shortBlocks := 0
	totalBlocks := len(blocks)
	
	if totalBlocks == 0 { return false }

	for _, b := range blocks {
		if b.Type == "heading1" || b.Type == "heading2" || b.Type == "heading3" {
			headings++
		}
		if estimateTokens(b.Content) < 30 {
			shortBlocks++
		}
	}

	headingRatio := float64(headings) / float64(totalBlocks)
	noStructure := headingRatio < 0.02
	longEnough := totalBlocks > 20
	notSlide := float64(shortBlocks)/float64(totalBlocks) <= 0.7

	return noStructure && longEnough && notSlide
}

func chunkWithGroqFallback(blocks []Block) []Chunk {
	// Reconstruct plain text first
	var fullText string
	for _, b := range blocks {
		fullText += b.Content + "\n\n"
	}

	// Ask Groq to add basic Markdown headers (Structure Enhancement)
	sysPrompt := `Bạn là công cụ tiền xử lý và cấu trúc văn bản học thuật. Nhiệm vụ của bạn là tái cấu trúc văn bản sau bằng Markdown headers (#, ##, ###) và gắn tag đặc biệt.

QUY TẮC:
1. Giữ nguyên nội dung gốc, không thêm lời chào hay giải thích.
2. Đánh dấu các bảng biểu với tag [TABLE_START] và [TABLE_END]. Nếu bảng quá phức tạp, dùng thêm [TABLE_COMPLEX].
3. Đánh dấu các đoạn code với tag [CODE_START] và [CODE_END].
4. XỬ LÝ EDGE CASES:
 - OCR noise (chuỗi vô nghĩa): Giữ nguyên, bọc trong [OCR_NOISE_START]...[OCR_NOISE_END].
 - Công thức LaTeX: Giữ nguyên 100%, bọc trong [FORMULA_START]...[FORMULA_END].
 - Nếu không rõ cấu trúc tiêu đề: Trả về text gốc và thêm [STRUCTURE_UNCLEAR] ở đầu đoạn.`
	
	textToProcess := fullText
	if len(textToProcess) > 15000 {
		textToProcess = textToProcess[:15000] // Avoid extreme token usage if too large
	}

	messages := []ChatMessage{
		{Role: "system", Content: sysPrompt},
		{Role: "user", Content: textToProcess},
	}
	
	start := time.Now()
	// Use Groq specifically for speed fallback
	res, _, err := AI.ChatNonStream(ServiceSummary, messages) // Summary usually routes to faster models
	if err != nil || len(res) < 100 {
		log.Printf("⚠️ Groq fallback failed or returned too little data (%v). Using standard text splitting.", err)
		return fallbackToStandardSplit(fullText)
	}

	log.Printf("✨ Groq enhancement took %v", time.Since(start))
	
	// Re-parse Groq result into fake blocks by scanning lines
	lines := strings.Split(res, "\n")
	var groqBlocks []Block
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" { continue }
		if strings.HasPrefix(line, "### ") {
			groqBlocks = append(groqBlocks, Block{Type: "heading3", Content: strings.TrimPrefix(line, "### ")})
		} else if strings.HasPrefix(line, "## ") {
			groqBlocks = append(groqBlocks, Block{Type: "heading2", Content: strings.TrimPrefix(line, "## ")})
		} else if strings.HasPrefix(line, "# ") {
			groqBlocks = append(groqBlocks, Block{Type: "heading1", Content: strings.TrimPrefix(line, "# ")})
		} else {
			groqBlocks = append(groqBlocks, Block{Type: "paragraph", Content: line})
		}
	}

	return buildChunks(groqBlocks)
}

func fallbackToStandardSplit(text string) []Chunk {
	textChunks := SplitIntoChunks(text, 500, 50)
	var finalChunks []Chunk
	for i, c := range textChunks {
		finalChunks = append(finalChunks, Chunk{
			Content:          c,
			RetrievalContent: c,
			TokenCount:       estimateTokens(c),
			ChunkIndex:       i,
		})
	}
	return finalChunks
}

// Helpers

func estimateTokens(text string) int {
	return len(text) / 4
}

func getLastSentences(text string, n int) string {
	sentences := splitSentences(text)
	if len(sentences) <= n {
		return ""
	}
	return strings.Join(sentences[len(sentences)-n:], " ")
}

func splitSentences(text string) []string {
	var sentences []string
	var current strings.Builder
	runes := []rune(text)

	for i, r := range runes {
		current.WriteRune(r)
		if r == '.' || r == '!' || r == '?' {
			if i+2 < len(runes) && runes[i+1] == ' ' && isUpperVietnamese(runes[i+2]) {
				sentences = append(sentences, strings.TrimSpace(current.String()))
				current.Reset()
			}
		}
	}
	if current.Len() > 0 {
		sentences = append(sentences, strings.TrimSpace(current.String()))
	}
	return sentences
}

func isUpperVietnamese(r rune) bool {
	uppers := "ABCDEFGHIJKLMNOPQRSTUVWXYZÁÀẢÃẠĂẮẶẰẲẴÂẤẬẦẨẪĐÊẾỆỀỂỄÔỐỘỒỔỖƠỚỢỜỞỠƯỨỰỪỬỮ"
	return strings.ContainsRune(uppers, r)
}

func min(a, b int) int {
	if a < b { return a }
	return b
}

func shortenBreadcrumb(content string) string {
	// 1. Remove numbering prefixes (e.g., "1.2.3 Section Name" -> "Section Name")
	content = strings.TrimSpace(content)
	words := strings.Fields(content)
	if len(words) == 0 {
		return ""
	}

	// Check if the first word is a numbering (e.g., "1.", "1.2", "1.2.3")
	firstWord := words[0]
	isNumbering := true
	for _, r := range firstWord {
		if (r < '0' || r > '9') && r != '.' {
			isNumbering = false
			break
		}
	}

	if isNumbering && len(words) > 1 {
		words = words[1:]
	}

	// 2. Keep max 4 important keywords
	if len(words) > 4 {
		words = words[:4]
	}

	return strings.Join(words, " ")
}

func needsOverlap(currentBlockType string, prevBlockContent string) bool {
	// Skip overlap if current block is a heading or if previous block ended with a heading mark
	if strings.HasPrefix(currentBlockType, "heading") {
		return false
	}
	if strings.HasSuffix(strings.TrimSpace(prevBlockContent), "#") {
		return false
	}
	return true
}

func buildRetrievalContent(breadcrumb string, overlap string, content string) string {
	var parts []string
	if breadcrumb != "" {
		parts = append(parts, breadcrumb)
	}
	if overlap != "" {
		parts = append(parts, overlap)
	}
	parts = append(parts, content)
	return strings.Join(parts, "\n\n")
}
