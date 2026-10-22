// Package doctext turns an activity document (.pdf, .docx, .txt, .md) into
// plain text. The folder and HTTP document adapters share it, so a file is
// read the same way wherever it comes from.
package doctext

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	// MaxFileBytes is the largest document that is opened.
	MaxFileBytes = 50 << 20
	// maxDocxXMLBytes bounds the text part of a .docx once unzipped, so a
	// small hostile archive cannot expand without limit.
	maxDocxXMLBytes = 30 << 20
)

// PDFTextExtractor turns a PDF file into text; websearch.PDFToText fits.
type PDFTextExtractor interface {
	ExtractText(ctx context.Context, path string) (string, error)
}

// Extractor reads documents. PDF may be nil when pdftotext is not
// installed: PDFs then fail with an error and the report flags them.
type Extractor struct {
	PDF PDFTextExtractor
}

// Supported reports whether a file name has an extension Text can read, and
// is not a hidden or Word lock file.
func Supported(name string) bool {
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "~$") {
		return false
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf", ".docx", ".txt", ".md":
		return true
	}
	return false
}

// Text reads the file at path as text, by its extension.
func (e Extractor) Text(ctx context.Context, path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() > MaxFileBytes {
		return "", fmt.Errorf("file is %d bytes, over the %d limit", info.Size(), MaxFileBytes)
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf":
		if e.PDF == nil {
			return "", errors.New("pdf support is not available (install poppler-utils for pdftotext)")
		}
		text, err := e.PDF.ExtractText(ctx, path)
		return strings.ReplaceAll(text, "\f", "\n"), err
	case ".docx":
		return docxText(path)
	default:
		b, err := os.ReadFile(path)
		return string(b), err
	}
}

// docxText extracts the paragraphs of a .docx: the text of every w:t, with
// a newline after each paragraph, tab and line break.
func docxText(path string) (string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", fmt.Errorf("not a .docx file: %w", err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != "word/document.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		defer rc.Close()
		return wordXMLText(io.LimitReader(rc, maxDocxXMLBytes))
	}
	return "", errors.New("not a .docx file: no word/document.xml")
}

func wordXMLText(r io.Reader) (string, error) {
	dec := xml.NewDecoder(r)
	var b strings.Builder
	inText := false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return strings.TrimSpace(b.String()), nil
		}
		if err != nil {
			return "", fmt.Errorf("read word/document.xml: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "tab":
				b.WriteByte('\t')
			case "br", "cr":
				b.WriteByte('\n')
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				b.WriteByte('\n')
			}
		case xml.CharData:
			if inText {
				b.Write(t)
			}
		}
	}
}
