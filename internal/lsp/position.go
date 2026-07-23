package lsp

import (
	"fmt"
	"net/url"
	"strings"
)

// pathToURI converts an absolute filesystem path to a file:// URI with correct
// percent-encoding, as language servers expect.
func pathToURI(absPath string) string {
	u := url.URL{Scheme: "file", Path: absPath}
	return u.String()
}

// uriToPath converts a file:// URI back to a filesystem path, best-effort.
func uriToPath(uri string) string {
	if u, err := url.Parse(uri); err == nil && u.Scheme == "file" {
		return u.Path
	}
	return strings.TrimPrefix(uri, "file://")
}

// utf16Len returns the number of UTF-16 code units in s. LSP character offsets
// are counted in UTF-16, so an astral-plane rune (e.g. an emoji) counts as two.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// locateSymbol computes the LSP position of symbol on a 1-based line of source.
// The model works from read_file's line-numbered output, so it can name the
// line and the identifier without computing UTF-16 columns itself; this finds
// the identifier on that line and converts the byte offset to a UTF-16 one.
//
// A zero or negative line falls back to searching the whole file for the first
// occurrence, so a position can still be resolved when the model omits it.
func locateSymbol(content, symbol string, line int) (Position, error) {
	if symbol == "" {
		return Position{}, fmt.Errorf("symbol must not be empty")
	}
	lines := strings.Split(content, "\n")

	if line >= 1 && line <= len(lines) {
		if col := strings.Index(lines[line-1], symbol); col >= 0 {
			return Position{Line: line - 1, Character: utf16Len(lines[line-1][:col])}, nil
		}
		return Position{}, fmt.Errorf("symbol %q not found on line %d", symbol, line)
	}

	for i, text := range lines {
		if col := strings.Index(text, symbol); col >= 0 {
			return Position{Line: i, Character: utf16Len(text[:col])}, nil
		}
	}
	return Position{}, fmt.Errorf("symbol %q not found in file", symbol)
}
