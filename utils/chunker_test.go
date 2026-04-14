package utils

import (
	"testing"
)

func TestShortenBreadcrumb(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"1.2.3 Giới thiệu về AI", "Giới thiệu về AI"},
		{"Section 1: Overview", "Section 1: Overview"},
		{"Hướng dẫn sử dụng phần mềm Mindex hệ thống", "Hướng dẫn sử dụng"},
		{"   4.5   Khoảng trắng thừa   ", "Khoảng trắng thừa"},
		{"", ""},
	}

	for _, tt := range tests {
		result := shortenBreadcrumb(tt.input)
		if result != tt.expected {
			t.Errorf("shortenBreadcrumb(%q) = %q; want %q", tt.input, result, tt.expected)
		}
	}
}

func TestNeedsOverlap(t *testing.T) {
	tests := []struct {
		currentType string
		prevContent string
		expected    bool
	}{
		{"paragraph", "Nội dung trước đó.", true},
		{"heading1", "Nội dung trước đó.", false},
		{"paragraph", "Tiêu đề kết thúc bằng #", false},
		{"heading2", "Bất kỳ nội dung nào", false},
	}

	for _, tt := range tests {
		result := needsOverlap(tt.currentType, tt.prevContent)
		if result != tt.expected {
			t.Errorf("needsOverlap(%q, %q) = %v; want %v", tt.currentType, tt.prevContent, result, tt.expected)
		}
	}
}

func TestBuildRetrievalContent(t *testing.T) {
	breadcrumb := "[Chương 1 > Mở đầu]"
	overlap := "...kết thúc đoạn trước."
	content := "Nội dung đoạn hiện tại."

	result := buildRetrievalContent(breadcrumb, overlap, content)
	expected := breadcrumb + "\n\n" + overlap + "\n\n" + content

	if result != expected {
		t.Errorf("buildRetrievalContent() = %q; want %q", result, expected)
	}

	// Test case without overlap
	resultNoOverlap := buildRetrievalContent(breadcrumb, "", content)
	expectedNoOverlap := breadcrumb + "\n\n" + content
	if resultNoOverlap != expectedNoOverlap {
		t.Errorf("buildRetrievalContent(no overlap) = %q; want %q", resultNoOverlap, expectedNoOverlap)
	}
}
