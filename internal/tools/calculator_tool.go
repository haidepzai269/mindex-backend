package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// CalculatorTool evaluates basic arithmetic expressions safely (no eval/
// arbitrary code execution), addressing US1's independent test and US4's
// "LLM thường tính sai số học" problem.
type CalculatorTool struct{}

func NewCalculatorTool() *CalculatorTool { return &CalculatorTool{} }

func (t *CalculatorTool) Name() string { return "calculator" }
func (t *CalculatorTool) Description() string {
	return "Tính toán biểu thức số học (cộng, trừ, nhân, chia, ngoặc đơn, phần trăm) hoặc đổi đơn vị đo (độ dài, khối lượng, thể tích). Dùng khi câu hỏi cần kết quả số chính xác."
}
func (t *CalculatorTool) Category() ToolCategory { return CategoryComputationExecution }
func (t *CalculatorTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"expression": map[string]interface{}{
				"type": "string",
				"description": "Biểu thức số học (ví dụ \"1247 * 893\", \"15% * 2350000\") " +
					"hoặc lệnh đổi đơn vị dạng \"<số> <đơn vị> to <đơn vị>\" (ví dụ \"10 km to m\"). " +
					"Đơn vị hỗ trợ: km/m/cm/mm (độ dài), kg/g/t (khối lượng), l/ml (thể tích).",
			},
		},
		"required": []string{"expression"},
	}
}
func (t *CalculatorTool) Timeout() time.Duration     { return 2 * time.Second }
func (t *CalculatorTool) TierLimit() map[string]int  { return nil }
func (t *CalculatorTool) RequiresConfirmation() bool { return false }

type calculatorInput struct {
	Expression string `json:"expression"`
}

func (t *CalculatorTool) Execute(ctx context.Context, execCtx ToolExecutionContext, input json.RawMessage) (ToolResult, error) {
	var in calculatorInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{}, fmt.Errorf("calculator: invalid input: %v", err)
	}

	if result, ok, err := tryUnitConversion(in.Expression); ok {
		if err != nil {
			return ToolResult{}, fmt.Errorf("calculator: %v", err)
		}
		return ToolResult{TextOutput: formatNumber(result)}, nil
	}

	result, err := evaluateArithmetic(in.Expression)
	if err != nil {
		return ToolResult{}, fmt.Errorf("calculator: %v", err)
	}
	return ToolResult{TextOutput: formatNumber(result)}, nil
}

// unitToBase maps a supported unit to its value in the base unit of its
// category (meters for length, grams for weight, liters for volume).
var unitToBase = map[string]float64{
	"km": 1000, "m": 1, "cm": 0.01, "mm": 0.001,
	"t": 1000000, "kg": 1000, "g": 1,
	"l": 1, "ml": 0.001,
}

var unitCategory = map[string]string{
	"km": "length", "m": "length", "cm": "length", "mm": "length",
	"t": "weight", "kg": "weight", "g": "weight",
	"l": "volume", "ml": "volume",
}

var unitConversionPattern = regexp.MustCompile(`(?i)^\s*([0-9.]+)\s*(km|cm|mm|kg|ml|m|t|g|l)\s*(?:to|sang|->)\s*(km|cm|mm|kg|ml|m|t|g|l)\s*$`)

// tryUnitConversion checks whether expr matches the "<number> <unit> to
// <unit>" syntax (US4 AC2). The bool return reports whether the expression
// was recognized as a conversion at all, so the caller can fall back to
// evaluateArithmetic for plain arithmetic expressions.
func tryUnitConversion(expr string) (float64, bool, error) {
	matches := unitConversionPattern.FindStringSubmatch(strings.TrimSpace(expr))
	if matches == nil {
		return 0, false, nil
	}
	value, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return 0, true, fmt.Errorf("invalid number %q", matches[1])
	}
	fromUnit, toUnit := strings.ToLower(matches[2]), strings.ToLower(matches[3])
	if unitCategory[fromUnit] != unitCategory[toUnit] {
		return 0, true, fmt.Errorf("không thể đổi giữa hai đơn vị khác loại (%s và %s)", fromUnit, toUnit)
	}
	return value * unitToBase[fromUnit] / unitToBase[toUnit], true, nil
}

func formatNumber(v float64) string {
	if v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// evaluateArithmetic parses and evaluates a +-*/() expression using a small
// recursive-descent parser. Only digits, '.', '+', '-', '*', '/', '(', ')'
// and whitespace are accepted - anything else is rejected before parsing,
// so this can never execute arbitrary code (US4 AC3).
func evaluateArithmetic(expr string) (float64, error) {
	cleaned := strings.TrimSpace(expr)
	if cleaned == "" {
		return 0, fmt.Errorf("empty expression")
	}
	for _, r := range cleaned {
		if !strings.ContainsRune("0123456789.+-*/()% \t", r) {
			return 0, fmt.Errorf("unsupported character %q in expression", r)
		}
	}
	p := &exprParser{input: cleaned}
	p.skipSpace()
	val, err := p.parseExpr()
	if err != nil {
		return 0, err
	}
	p.skipSpace()
	if p.pos != len(p.input) {
		return 0, fmt.Errorf("unexpected trailing characters at position %d", p.pos)
	}
	return val, nil
}

type exprParser struct {
	input string
	pos   int
}

func (p *exprParser) skipSpace() {
	for p.pos < len(p.input) && (p.input[p.pos] == ' ' || p.input[p.pos] == '\t') {
		p.pos++
	}
}

func (p *exprParser) peek() byte {
	if p.pos >= len(p.input) {
		return 0
	}
	return p.input[p.pos]
}

func (p *exprParser) parseExpr() (float64, error) {
	val, err := p.parseTerm()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpace()
		switch p.peek() {
		case '+':
			p.pos++
			rhs, err := p.parseTerm()
			if err != nil {
				return 0, err
			}
			val += rhs
		case '-':
			p.pos++
			rhs, err := p.parseTerm()
			if err != nil {
				return 0, err
			}
			val -= rhs
		default:
			return val, nil
		}
	}
}

func (p *exprParser) parseTerm() (float64, error) {
	val, err := p.parseFactor()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpace()
		switch p.peek() {
		case '*':
			p.pos++
			rhs, err := p.parseFactor()
			if err != nil {
				return 0, err
			}
			val *= rhs
		case '/':
			p.pos++
			rhs, err := p.parseFactor()
			if err != nil {
				return 0, err
			}
			if rhs == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			val /= rhs
		default:
			return val, nil
		}
	}
}

// parseFactor parses one factor and then applies any trailing '%' postfix
// operators (US4: "15%" means 0.15, so "2350000 * 15%" = 352500 - the
// calculator-convention reading of percent, not a modulo operator).
func (p *exprParser) parseFactor() (float64, error) {
	val, err := p.parseFactorBase()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpace()
		if p.peek() != '%' {
			return val, nil
		}
		p.pos++
		val /= 100
	}
}

func (p *exprParser) parseFactorBase() (float64, error) {
	p.skipSpace()
	switch p.peek() {
	case '-':
		p.pos++
		val, err := p.parseFactor()
		if err != nil {
			return 0, err
		}
		return -val, nil
	case '(':
		p.pos++
		val, err := p.parseExpr()
		if err != nil {
			return 0, err
		}
		p.skipSpace()
		if p.peek() != ')' {
			return 0, fmt.Errorf("missing closing parenthesis")
		}
		p.pos++
		return val, nil
	}

	start := p.pos
	for p.pos < len(p.input) && (p.input[p.pos] >= '0' && p.input[p.pos] <= '9' || p.input[p.pos] == '.') {
		p.pos++
	}
	if start == p.pos {
		return 0, fmt.Errorf("expected number at position %d", p.pos)
	}
	val, err := strconv.ParseFloat(p.input[start:p.pos], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number %q", p.input[start:p.pos])
	}
	return val, nil
}
