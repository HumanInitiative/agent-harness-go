package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

type calculatorArgs struct {
	// Expression is a plain arithmetic expression, e.g. "(3 + 4) * 2".
	Expression string `json:"expression"`
}

// CalculatorTool evaluates arithmetic expressions. It exists both as a
// genuinely useful capability and as a second, slightly less trivial
// example of the outbound.ToolHandler pattern: unlike CurrentTimeTool it
// must validate and reject malformed model-supplied input.
type CalculatorTool struct{}

// NewCalculatorTool constructs a CalculatorTool.
func NewCalculatorTool() *CalculatorTool { return &CalculatorTool{} }

func (t *CalculatorTool) Name() string { return "calculator" }

func (t *CalculatorTool) Description() string {
	return "Evaluates a basic arithmetic expression (+, -, *, /, parentheses) and returns the " +
		"numeric result. Use this instead of doing arithmetic yourself whenever precision matters."
}

func (t *CalculatorTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"expression": map[string]any{
				"type":        "string",
				"description": "Arithmetic expression to evaluate, e.g. \"(3 + 4) * 2\".",
			},
		},
		"required": []string{"expression"},
	}
}

func (t *CalculatorTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var in calculatorArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("calculator: invalid arguments: %w", err)
	}
	if in.Expression == "" {
		return "", fmt.Errorf("calculator: expression must not be empty")
	}

	result, err := evalArithmetic(in.Expression)
	if err != nil {
		return "", fmt.Errorf("calculator: %w", err)
	}

	return fmt.Sprintf("%g", result), nil
}
