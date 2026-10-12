package rag

import "strings"

// Chunk splits text into ~size-rune pieces with overlap runes carried over
// between neighbors, preferring to cut at paragraph/sentence boundaries
// (looking back up to 20% of the chunk for one). Values proven in the old
// Node console: size=500, overlap=50.
func Chunk(text string, size, overlap int) []string {
	if size <= 0 {
		size = 500
	}
	if overlap < 0 || overlap >= size {
		overlap = size / 10
	}
	runes := []rune(strings.TrimSpace(text))
	if len(runes) == 0 {
		return nil
	}
	var out []string
	start := 0
	for start < len(runes) {
		end := start + size
		if end >= len(runes) {
			end = len(runes)
		} else {
			// prefer a natural boundary in the last 20% of the window
			for i := end; i > end-size/5 && i > start; i-- {
				switch runes[i-1] {
				case '\n', '。', '！', '？', '；', '.', '!', '?', ';':
					end = i
					i = start // break outer
				}
			}
		}
		piece := strings.TrimSpace(string(runes[start:end]))
		if piece != "" {
			out = append(out, piece)
		}
		if end == len(runes) {
			break
		}
		start = end - overlap
	}
	return out
}
