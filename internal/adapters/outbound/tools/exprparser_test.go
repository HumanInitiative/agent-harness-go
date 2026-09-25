package tools

import (
	"strings"
	"testing"
)

func TestEvalArithmetic_ValidExpressions(t *testing.T) {
	cases := []struct {
		expr string
		want float64
	}{
		{"1 + 2", 3},
		{"2 * 3 + 4", 10},
		{"2 + 3 * 4", 14},
		{"(2 + 3) * 4", 20},
		{"10 / 4", 2.5},
		{"-5 + 3", -2},
		{"3 - -2", 5},
		{"((1 + 2) * (3 + 4))", 21},
		{"  1   +   1  ", 2},
		{"3.5 * 2", 7},
	}

	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := evalArithmetic(tc.expr)
			if err != nil {
				t.Fatalf("evalArithmetic(%q) returned error: %v", tc.expr, err)
			}
			if got != tc.want {
				t.Errorf("evalArithmetic(%q) = %v, want %v", tc.expr, got, tc.want)
			}
		})
	}
}

func TestEvalArithmetic_Errors(t *testing.T) {
	cases := []string{
		"1 +",
		"(1 + 2",
		"1 + 2)",
		"1 / 0",
		"",
		"1 ++ 2",
		"abc",
		"1 + 2 foo",
	}

	for _, expr := range cases {
		t.Run(expr, func(t *testing.T) {
			if _, err := evalArithmetic(expr); err == nil {
				t.Errorf("evalArithmetic(%q) expected error, got none", expr)
			}
		})
	}
}

func TestEvalArithmetic_RejectsPathologicalInput(t *testing.T) {
	cases := map[string]string{
		"deep parentheses":     strings.Repeat("(", 200) + "1" + strings.Repeat(")", 200),
		"deep unary minus":     strings.Repeat("-", 200) + "1",
		"too long":             strings.Repeat("1+", 300) + "1",
		"overflow to infinity": "1" + strings.Repeat("0", 300) + " * 1" + strings.Repeat("0", 100),
	}
	for name, expr := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := evalArithmetic(expr); err == nil {
				t.Errorf("expected an error")
			}
		})
	}
}

func TestEvalArithmetic_AllowsNestingWithinLimit(t *testing.T) {
	expr := strings.Repeat("(", 30) + "2" + strings.Repeat(")", 30)
	got, err := evalArithmetic(expr)
	if err != nil || got != 2 {
		t.Fatalf("evalArithmetic = %v, %v; want 2, nil", got, err)
	}
}
