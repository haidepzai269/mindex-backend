package controllers

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestSanitizeUploadFilename(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "keeps safe basename", in: `C:\fakepath\Lecture 01.pdf`, want: "Lecture 01.pdf"},
		{name: "replaces unsafe characters", in: "bai<>tap?.docx", want: "bai_tap_.docx"},
		{name: "truncates long names", in: strings.Repeat("a", 300) + ".pdf", want: strings.Repeat("a", 160) + ".pdf"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeUploadFilename(tt.in); got != tt.want {
				t.Fatalf("sanitizeUploadFilename() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateDocumentSignature(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		size     int64
		header   []byte
		wantExt  string
		wantCode string
	}{
		{name: "valid pdf", filename: "lecture.pdf", size: 10, header: []byte("%PDF-1.7"), wantExt: ".pdf"},
		{name: "valid docx", filename: "lecture.docx", size: 10, header: []byte{'P', 'K', 0x03, 0x04, 0x14}, wantExt: ".docx"},
		{name: "reject txt", filename: "notes.txt", size: 10, header: []byte("plain text"), wantCode: "UNSUPPORTED_FILE_TYPE"},
		{name: "reject fake pdf", filename: "lecture.pdf", size: 10, header: []byte("not pdf"), wantCode: "INVALID_FILE_SIGNATURE"},
		{name: "reject too large", filename: "lecture.pdf", size: maxDocumentUploadSize + 1, header: []byte("%PDF"), wantCode: "FILE_TOO_LARGE"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validateDocumentSignature(tt.filename, tt.size, func() ([]byte, error) {
				return tt.header, nil
			})
			if tt.wantCode == "" {
				if err != nil {
					t.Fatalf("validateDocumentSignature() unexpected error: %v", err)
				}
				if got.Extension != tt.wantExt {
					t.Fatalf("Extension = %q, want %q", got.Extension, tt.wantExt)
				}
				return
			}

			var validationErr *uploadValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("error = %T %v, want *uploadValidationError", err, err)
			}
			if validationErr.Code != tt.wantCode {
				t.Fatalf("Code = %q, want %q", validationErr.Code, tt.wantCode)
			}
			if tt.wantCode == "FILE_TOO_LARGE" && validationErr.Status != http.StatusRequestEntityTooLarge {
				t.Fatalf("Status = %d, want %d", validationErr.Status, http.StatusRequestEntityTooLarge)
			}
		})
	}
}
