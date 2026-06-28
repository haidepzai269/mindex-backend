package utils

import (
	"encoding/json"
	"fmt"
	"time"
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

func RunImageOCR(imageURL string) (*OCRResult, error) {
	if Processing == nil {
		return nil, fmt.Errorf("processing client not initialized")
	}

	payload := map[string]any{
		"image_url": imageURL,
	}

	raw, err := Processing.CallAsync("ocr", payload, 45*time.Second)
	if err != nil {
		return nil, fmt.Errorf("processing service OCR failed: %w", err)
	}

	var result OCRResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("parse OCR result: %w", err)
	}
	return &result, nil
}
