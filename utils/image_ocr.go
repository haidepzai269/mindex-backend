package utils

import (
	"encoding/json"
	"fmt"
	"os/exec"
)

type OCRResult struct {
	Text    string     `json:"text"`
	Preview string     `json:"preview"`
	Blocks  []OCRBlock `json:"blocks"`
}

type OCRBlock struct {
	Text       string  `json:"text"`
	Confidence float64 `json:"confidence"`
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	W          float64 `json:"w"`
	H          float64 `json:"h"`
}

func RunImageOCR(imagePath string) (*OCRResult, error) {
	commands := []string{"py", "python", "python3"}
	var out []byte
	var err error

	for _, pyCmd := range commands {
		cmd := exec.Command(pyCmd, "image_ocr.py", imagePath)
		out, err = cmd.Output()
		if err == nil {
			break
		}
		if _, isExitError := err.(*exec.ExitError); isExitError {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("OCR script failed: %w", err)
	}

	var result OCRResult
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("OCR output parse error: %w", err)
	}
	return &result, nil
}
