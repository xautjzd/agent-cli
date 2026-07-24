// Package repl implements the interactive session: slash commands for
// controlling the agent (model, provider, skills, memory, config) and
// @path file references, following the conventions popularized by
// Claude Code, Codex CLI and pi.
package repl

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// fileRefRe matches @path tokens. The '@' must begin the line or follow a
// non-word rune (\w is ASCII here), so CJK prompts like "看一下@main.go" — which
// carry no space before '@' — resolve, while an email's "user@host" does not.
// The path may not contain whitespace or a second '@'; trailing sentence
// punctuation is trimmed afterwards so "@main.go," references main.go.
var fileRefRe = regexp.MustCompile(`(^|[^\w@])@([^\s@]+)`)

// maxRefBytes caps how much of one referenced file is inlined, protecting
// the context window from an accidental "@huge.log".
const maxRefBytes = 100 * 1024

// imageExtensions marks @refs that are attached as image parts rather than
// inlined as text.
var imageExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
}

// isImagePath reports whether a path looks like an image file.
func isImagePath(p string) bool {
	return imageExtensions[strings.ToLower(filepath.Ext(p))]
}

// ExpandRefs is ExpandFileRefs plus image awareness: @refs pointing at
// image files are returned as paths (for multimodal attachment) instead of
// being inlined as text, which would corrupt the prompt with binary data.
func ExpandRefs(input, workDir string) (text string, imagePaths []string, err error) {
	matches := fileRefRe.FindAllStringSubmatch(input, -1)
	var textInput = input
	seen := map[string]bool{}
	for _, m := range matches {
		ref := strings.TrimRight(m[2], `,.;:!?)"'`)
		if ref == "" || seen[ref] || !isImagePath(ref) {
			continue
		}
		seen[ref] = true
		path := ref
		if !filepath.IsAbs(path) {
			path = filepath.Join(workDir, path)
		}
		if _, err := os.Stat(path); err != nil {
			return "", nil, fmt.Errorf("@%s: %w", ref, err)
		}
		imagePaths = append(imagePaths, path)
		// Blank the ref in the copy handed to text expansion so the image
		// is not re-processed there; the mention stays in the visible text.
	}
	text, err = expandTextRefs(textInput, workDir, seen)
	if err != nil {
		return "", nil, err
	}
	return text, imagePaths, nil
}

// ExpandFileRefs finds @path references in the input, reads each file and
// appends its content to the message as clearly delimited blocks. The
// original @mentions stay in the text so the model sees what the user
// pointed at. Referencing a directory inlines its listing instead.
//
// A missing path is an error: silently sending a prompt whose reference
// failed to resolve would make the model hallucinate the file's contents.
func ExpandFileRefs(input, workDir string) (string, error) {
	return expandTextRefs(input, workDir, nil)
}

// expandTextRefs inlines non-image @refs; refs listed in skip (already
// handled as image attachments) are left untouched.
func expandTextRefs(input, workDir string, skip map[string]bool) (string, error) {
	matches := fileRefRe.FindAllStringSubmatch(input, -1)
	if len(matches) == 0 {
		return input, nil
	}

	var blocks []string
	seen := map[string]bool{}
	for _, m := range matches {
		ref := strings.TrimRight(m[2], `,.;:!?)"'`)
		if ref == "" || seen[ref] || skip[ref] {
			continue
		}
		seen[ref] = true

		path := ref
		if !filepath.IsAbs(path) {
			path = filepath.Join(workDir, path)
		}
		info, err := os.Stat(path)
		if err != nil {
			return "", fmt.Errorf("@%s: %w", ref, err)
		}

		if info.IsDir() {
			entries, err := os.ReadDir(path)
			if err != nil {
				return "", fmt.Errorf("@%s: %w", ref, err)
			}
			var names []string
			for _, e := range entries {
				name := e.Name()
				if e.IsDir() {
					name += "/"
				}
				names = append(names, name)
			}
			blocks = append(blocks, fmt.Sprintf("--- Referenced directory: %s ---\n%s",
				ref, strings.Join(names, "\n")))
			continue
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("@%s: %w", ref, err)
		}
		content := string(data)
		if len(content) > maxRefBytes {
			content = content[:maxRefBytes] + "\n... (file truncated at 100KB)"
		}
		blocks = append(blocks, fmt.Sprintf("--- Referenced file: %s ---\n%s", ref, content))
	}

	if len(blocks) == 0 {
		return input, nil
	}
	return input + "\n\n" + strings.Join(blocks, "\n\n"), nil
}
