// Package knowledge is the offline/design-time side of RAG: document
// parsing and the async ingestion queue (parse -> chunk -> embed -> store).
// The online query path lives in internal/rag.
package knowledge

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const MaxUploadBytes = 10 << 20 // 10MB

// Parse extracts plain text from an uploaded file. P4 supports .txt and .md
// (already plain text); richer formats (pdf/docx/OCR) are the documented EE
// sidecar seam.
func Parse(filename string, data []byte) (string, error) {
	if !utf8.Valid(data) {
		return "", fmt.Errorf("%s is not valid UTF-8 text", filename)
	}
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".txt", ".md", ".markdown":
		return strings.TrimSpace(string(data)), nil
	default:
		return "", fmt.Errorf("unsupported file type %q (only .txt / .md)", filepath.Ext(filename))
	}
}
