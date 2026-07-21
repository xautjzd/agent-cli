package provider

import "strings"

// visionSubstrings identify model families that accept image input.
// Matching is by lowercase substring, so versioned names ("gpt-4o-mini",
// "qwen2.5-vl-72b") are covered without enumerating every release.
var visionSubstrings = []string{
	"gpt-4o", "gpt-4.1", "gpt-4-turbo", "gpt-5", "chatgpt",
	"claude", "gemini", "gemma-3",
	"qwen-vl", "qwen2-vl", "qwen2.5-vl", "qvq",
	"glm-4v", "glm-4.5v", "pixtral", "llava", "minicpm-v", "internvl",
	"kimi-vl", "moonshot-v1-8k-vision", "step-1v", "yi-vision",
}

// visionPrefixes cover the OpenAI o-series, where substring matching would
// be too loose.
var visionPrefixes = []string{"o1", "o3", "o4"}

// SupportsVision reports whether a model is known to accept image input.
// Unknown models default to false — callers should offer a config override
// (profile "vision": true) for unrecognized vision-capable endpoints.
func SupportsVision(model string) bool {
	m := strings.ToLower(model)
	for _, s := range visionSubstrings {
		if strings.Contains(m, s) {
			return true
		}
	}
	for _, p := range visionPrefixes {
		if strings.HasPrefix(m, p) {
			return true
		}
	}
	return false
}
