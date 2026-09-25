package tools

import (
	"context"
	"testing"
)

func TestCalculatorTool_Execute(t *testing.T) {
	tool := NewCalculatorTool()

	got, err := tool.Execute(context.Background(), []byte(`{"expression": "2 * (3 + 4)"}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if got != "14" {
		t.Fatalf("Execute = %q, want %q", got, "14")
	}
}

func TestCalculatorTool_Execute_RejectsEmptyExpression(t *testing.T) {
	tool := NewCalculatorTool()
	if _, err := tool.Execute(context.Background(), []byte(`{"expression": ""}`)); err == nil {
		t.Fatal("expected error for empty expression, got nil")
	}
}

func TestCalculatorTool_Execute_RejectsMalformedJSON(t *testing.T) {
	tool := NewCalculatorTool()
	if _, err := tool.Execute(context.Background(), []byte(`not json`)); err == nil {
		t.Fatal("expected error for malformed arguments, got nil")
	}
}
