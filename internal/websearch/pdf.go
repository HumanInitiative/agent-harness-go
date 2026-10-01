package websearch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// PDFExtractor turns a PDF document into plain text.
type PDFExtractor interface {
	ExtractText(ctx context.Context, pdf []byte) (string, error)
}

// PDFToText extracts text by running poppler's pdftotext. It is optional:
// when pdftotext is not installed, NewPDFToText reports ErrPDFUnavailable and
// the fetcher returns that error for PDFs instead of failing at startup.
// CSR and sustainability reports are mostly PDFs, so installing poppler-utils
// is recommended for the CSR discovery work.
type PDFToText struct {
	path      string
	timeout   time.Duration
	maxOutput int
}

// NewPDFToText locates pdftotext on PATH.
func NewPDFToText(timeout time.Duration, maxOutputBytes int) (*PDFToText, error) {
	path, err := exec.LookPath("pdftotext")
	if err != nil {
		return nil, fmt.Errorf("%w: pdftotext not found on PATH (install poppler-utils)", ErrPDFUnavailable)
	}
	return NewPDFToTextAt(path, timeout, maxOutputBytes), nil
}

// NewPDFToTextAt uses the pdftotext binary at path.
func NewPDFToTextAt(path string, timeout time.Duration, maxOutputBytes int) *PDFToText {
	return &PDFToText{path: path, timeout: timeout, maxOutput: maxOutputBytes}
}

// ExtractText implements PDFExtractor. The PDF goes through a temporary file
// because pdftotext needs a seekable input.
func (p *PDFToText) ExtractText(ctx context.Context, pdf []byte) (string, error) {
	f, err := os.CreateTemp("", "websearch-*.pdf")
	if err != nil {
		return "", fmt.Errorf("websearch: pdf temp file: %w", err)
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(pdf); err != nil {
		f.Close()
		return "", fmt.Errorf("websearch: pdf temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("websearch: pdf temp file: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, p.path, "-enc", "UTF-8", "-q", f.Name(), "-")
	cmd.Stdout = &limitedBuffer{buf: &out, max: p.maxOutput}
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("websearch: pdftotext: %w", err)
	}
	return out.String(), nil
}

// limitedBuffer keeps at most max bytes and silently discards the rest, so a
// huge report cannot exhaust memory; the caller truncates further anyway.
type limitedBuffer struct {
	buf *bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.max - l.buf.Len(); room > 0 {
		if len(p) > room {
			l.buf.Write(p[:room])
		} else {
			l.buf.Write(p)
		}
	}
	return len(p), nil
}
