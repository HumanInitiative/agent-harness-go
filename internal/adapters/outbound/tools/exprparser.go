package tools

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

const (
	// maxExpressionLength bounds the input size the parser will look at.
	maxExpressionLength = 512
	// maxNestingDepth bounds recursion. Without it, an expression of a few
	// thousand '(' or '-' characters would recurse until the goroutine
	// stack blows up — and the input comes from a model, so it is untrusted.
	maxNestingDepth = 64
)

// evalArithmetic evaluates a plain arithmetic expression (+, -, *, /,
// parentheses, unary minus, decimal numbers) and returns its result.
//
// This exists so CalculatorTool never has to run untrusted text through
// Go's own evaluator or shell out to another process — both of which would
// turn a calculator into an arbitrary-code-execution tool the moment a
// model (or a prompt-injected document) feeds it something unexpected. A
// small hand-written recursive-descent parser only ever recognizes the
// grammar below, so "malicious input" degenerates to, at worst, a parse
// error.
//
//	expr   := term (('+' | '-') term)*
//	term   := factor (('*' | '/') factor)*
//	factor := '-' factor | number | '(' expr ')'
func evalArithmetic(expression string) (float64, error) {
	if len(expression) > maxExpressionLength {
		return 0, fmt.Errorf("expression is longer than %d characters", maxExpressionLength)
	}

	p := &exprParser{input: []rune(expression)}
	p.skipSpaces()
	value, err := p.parseExpr()
	if err != nil {
		return 0, err
	}
	p.skipSpaces()
	if p.pos != len(p.input) {
		return 0, fmt.Errorf("unexpected character %q at position %d", p.input[p.pos], p.pos)
	}
	if math.IsInf(value, 0) || math.IsNaN(value) {
		return 0, fmt.Errorf("result is out of range")
	}
	return value, nil
}

type exprParser struct {
	input []rune
	pos   int
	depth int
}

// enter records one more level of recursion and fails once the nesting
// limit is exceeded; every call must be paired with a deferred leave.
func (p *exprParser) enter() error {
	p.depth++
	if p.depth > maxNestingDepth {
		return fmt.Errorf("expression is nested more than %d levels deep", maxNestingDepth)
	}
	return nil
}

func (p *exprParser) leave() { p.depth-- }

func (p *exprParser) skipSpaces() {
	for p.pos < len(p.input) && unicode.IsSpace(p.input[p.pos]) {
		p.pos++
	}
}

func (p *exprParser) peek() (rune, bool) {
	if p.pos >= len(p.input) {
		return 0, false
	}
	return p.input[p.pos], true
}

func (p *exprParser) parseExpr() (float64, error) {
	value, err := p.parseTerm()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpaces()
		op, ok := p.peek()
		if !ok || (op != '+' && op != '-') {
			return value, nil
		}
		p.pos++
		rhs, err := p.parseTerm()
		if err != nil {
			return 0, err
		}
		if op == '+' {
			value += rhs
		} else {
			value -= rhs
		}
	}
}

func (p *exprParser) parseTerm() (float64, error) {
	value, err := p.parseFactor()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpaces()
		op, ok := p.peek()
		if !ok || (op != '*' && op != '/') {
			return value, nil
		}
		p.pos++
		rhs, err := p.parseFactor()
		if err != nil {
			return 0, err
		}
		if op == '*' {
			value *= rhs
		} else {
			if rhs == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			value /= rhs
		}
	}
}

// parseFactor is the only place recursion re-enters the grammar (unary
// minus and parenthesised sub-expressions), so guarding depth here bounds
// the whole parser.
func (p *exprParser) parseFactor() (float64, error) {
	if err := p.enter(); err != nil {
		return 0, err
	}
	defer p.leave()

	p.skipSpaces()
	ch, ok := p.peek()
	if !ok {
		return 0, fmt.Errorf("unexpected end of expression")
	}

	if ch == '-' {
		p.pos++
		value, err := p.parseFactor()
		if err != nil {
			return 0, err
		}
		return -value, nil
	}

	if ch == '(' {
		p.pos++
		value, err := p.parseExpr()
		if err != nil {
			return 0, err
		}
		p.skipSpaces()
		closing, ok := p.peek()
		if !ok || closing != ')' {
			return 0, fmt.Errorf("missing closing parenthesis")
		}
		p.pos++
		return value, nil
	}

	return p.parseNumber()
}

func (p *exprParser) parseNumber() (float64, error) {
	start := p.pos
	for p.pos < len(p.input) && (unicode.IsDigit(p.input[p.pos]) || p.input[p.pos] == '.') {
		p.pos++
	}
	if p.pos == start {
		return 0, fmt.Errorf("expected a number at position %d, got %q", start, string(p.input[start:]))
	}
	text := strings.TrimSpace(string(p.input[start:p.pos]))
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number %q: %w", text, err)
	}
	return value, nil
}
