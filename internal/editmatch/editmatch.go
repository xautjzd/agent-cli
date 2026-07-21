// Package editmatch performs context-anchored, whitespace-tolerant text
// replacement — the matching engine behind the edit_file tool.
//
// Exact string replacement is brittle: a single differing space, tab, or
// trailing-whitespace character makes the whole edit fail, even though a human
// can see the intended target unambiguously. Mainstream coding agents (Aider's
// SEARCH/REPLACE blocks, Claude Code's edit tool) solve this with a tiered
// fuzzy match that anchors on the surrounding lines and tolerates indentation
// differences, then re-indents the replacement to fit.
//
// The tiers, tried in order until one matches, are:
//
//  1. exact      — byte-for-byte substring (fastest, zero ambiguity).
//  2. line-trim  — lines equal after trimming leading/trailing whitespace;
//     the replacement is re-indented to the target's actual indentation.
//  3. ws-collapse— lines equal after collapsing all internal whitespace runs;
//     also re-indented. The most forgiving tier.
//
// Only whole-line tiers reflow indentation; the exact tier substitutes
// verbatim. When nothing matches, Replace returns an error that points at the
// closest region so the model can correct itself rather than guessing blindly.
package editmatch

import (
	"fmt"
	"strings"
)

// Result describes a successful replacement.
type Result struct {
	// Updated is the full new file content.
	Updated string
	// Count is how many occurrences were replaced.
	Count int
	// Strategy names the tier that matched: "exact", "line-trim", or
	// "ws-collapse". Reported so the user/model can see when a fuzzy match
	// (rather than an exact one) was applied.
	Strategy string
}

// Fuzzy reports whether the strategy was anything other than an exact match.
func (r Result) Fuzzy() bool { return r.Strategy != "" && r.Strategy != strategyExact }

const (
	strategyExact    = "exact"
	strategyLineTrim = "line-trim"
	strategyCollapse = "ws-collapse"
)

// Replace substitutes pattern with replacement in content, escalating through
// the tiers until one produces at least one match. When all is false the
// pattern must resolve to exactly one location (an ambiguous match is an error
// so an edit is never applied to the wrong place); when true every match is
// replaced. It returns an error if the pattern cannot be located.
func Replace(content, pattern, replacement string, all bool) (Result, error) {
	if pattern == "" {
		return Result{}, fmt.Errorf("old_string must not be empty")
	}

	// Tier 1: exact substring match.
	if n := strings.Count(content, pattern); n > 0 {
		if n > 1 && !all {
			return Result{}, ambiguityError(n)
		}
		updated := content
		if all {
			updated = strings.ReplaceAll(content, pattern, replacement)
		} else {
			updated = strings.Replace(content, pattern, replacement, 1)
		}
		return Result{Updated: updated, Count: n, Strategy: strategyExact}, nil
	}

	// Tiers 2 and 3 operate on lines, tolerating whitespace differences.
	for _, tier := range []struct {
		name  string
		canon func(string) string
	}{
		{strategyLineTrim, strings.TrimSpace},
		{strategyCollapse, collapseWS},
	} {
		if res, ok, err := lineReplace(content, pattern, replacement, all, tier.name, tier.canon); ok || err != nil {
			return res, err
		}
	}

	return Result{}, notFoundError(content, pattern)
}

// lineReplace runs one whole-line tier. canon canonicalizes a line for
// comparison (trim, or collapse). ok is false (with no error) when this tier
// found nothing, so the caller can try the next tier.
func lineReplace(content, pattern, replacement string, all bool, strategy string, canon func(string) string) (Result, bool, error) {
	cLines, offsets := splitWithOffsets(content)
	pLines := strings.Split(pattern, "\n")
	// A trailing newline in the pattern yields an empty final element; drop it
	// so the window size reflects the real lines to match.
	if len(pLines) > 0 && pLines[len(pLines)-1] == "" {
		pLines = pLines[:len(pLines)-1]
	}
	if len(pLines) == 0 {
		return Result{}, false, nil
	}

	canonP := make([]string, len(pLines))
	for i, l := range pLines {
		canonP[i] = canon(l)
	}

	// Find every non-overlapping window whose canonical lines equal the
	// pattern's.
	var starts []int
	for i := 0; i+len(pLines) <= len(cLines); i++ {
		if windowMatches(cLines, i, canonP, canon) {
			starts = append(starts, i)
			i += len(pLines) - 1 // skip past this match to avoid overlap
		}
	}
	if len(starts) == 0 {
		return Result{}, false, nil
	}
	if len(starts) > 1 && !all {
		return Result{}, true, ambiguityError(len(starts))
	}

	// Apply matches from last to first so earlier byte offsets stay valid.
	updated := content
	for k := len(starts) - 1; k >= 0; k-- {
		i := starts[k]
		start := offsets[i]
		end := offsets[i+len(pLines)] - 1 // byte just before the newline after the block
		reindented := reindent(replacement, pLines[0], cLines[i])
		updated = updated[:start] + reindented + updated[end:]
	}
	return Result{Updated: updated, Count: len(starts), Strategy: strategy}, true, nil
}

// windowMatches reports whether the content lines starting at i equal the
// canonicalized pattern lines.
func windowMatches(cLines []string, i int, canonP []string, canon func(string) string) bool {
	for j, want := range canonP {
		if canon(cLines[i+j]) != want {
			return false
		}
	}
	return true
}

// reindent shifts every line of replacement by the indentation difference
// between the pattern's first line and the matched line in the file, so a
// replacement written at any indentation lands correctly. The shift is only
// applied when one indent is a prefix of the other (the common case of a
// consistently indented or dedented block); otherwise the replacement is used
// as written.
func reindent(replacement, patternFirst, matchedFirst string) string {
	patLead := leadingWS(patternFirst)
	matchLead := leadingWS(matchedFirst)
	if patLead == matchLead {
		return replacement
	}

	var add, remove string
	switch {
	case strings.HasPrefix(matchLead, patLead):
		add = matchLead[len(patLead):] // target is more indented: prepend the extra
	case strings.HasPrefix(patLead, matchLead):
		remove = patLead[len(matchLead):] // target is less indented: strip the excess
	default:
		return replacement // incompatible indent styles; don't guess
	}

	lines := strings.Split(replacement, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue // leave blank lines blank
		}
		if remove != "" {
			l = strings.TrimPrefix(l, remove)
		}
		if add != "" {
			l = add + l
		}
		lines[i] = l
	}
	return strings.Join(lines, "\n")
}

// leadingWS returns the run of spaces/tabs at the start of s.
func leadingWS(s string) string {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return s[:i]
}

// collapseWS canonicalizes a line by trimming its ends and reducing every
// internal whitespace run to a single space, so token spacing differences do
// not defeat a match.
func collapseWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// splitWithOffsets splits content into lines (newline stripped) and returns,
// alongside them, the byte offset where each line begins; offsets has one
// extra trailing entry so offsets[i+n] locates the newline after line i+n-1.
func splitWithOffsets(content string) (lines []string, offsets []int) {
	lines = strings.Split(content, "\n")
	offsets = make([]int, len(lines)+1)
	off := 0
	for i, l := range lines {
		offsets[i] = off
		off += len(l) + 1 // +1 for the '\n' separator
	}
	offsets[len(lines)] = off
	return lines, offsets
}

// ambiguityError reports a non-unique match with actionable guidance.
func ambiguityError(n int) error {
	return fmt.Errorf("old_string matches %d locations; add surrounding context to make it unique, or set replace_all to change them all", n)
}

// notFoundError reports a miss and, when possible, points at the closest
// region in the file so the model can correct its old_string.
func notFoundError(content, pattern string) error {
	line, score := closestRegion(content, pattern)
	if line == 0 || score == 0 {
		return fmt.Errorf("old_string not found (no similar text in the file); re-read the file and copy the exact text to change")
	}
	return fmt.Errorf("old_string not found; the closest region starts near line %d (%d%% of lines similar). Re-read that area and copy the exact text, including punctuation", line, score)
}

// closestRegion scans for the window most similar to the pattern and returns
// its 1-based starting line and the percentage of lines that match after
// trimming. It is a diagnostic aid only — it never drives a replacement.
func closestRegion(content, pattern string) (line, percent int) {
	cLines := strings.Split(content, "\n")
	pLines := strings.Split(strings.TrimRight(pattern, "\n"), "\n")
	if len(pLines) == 0 || len(cLines) < len(pLines) {
		return 0, 0
	}
	canonP := make([]string, len(pLines))
	for i, l := range pLines {
		canonP[i] = strings.TrimSpace(l)
	}
	bestStart, bestHits := -1, 0
	for i := 0; i+len(pLines) <= len(cLines); i++ {
		hits := 0
		for j, want := range canonP {
			if strings.TrimSpace(cLines[i+j]) == want && want != "" {
				hits++
			}
		}
		if hits > bestHits {
			bestHits, bestStart = hits, i
		}
	}
	if bestStart < 0 || bestHits == 0 {
		return 0, 0
	}
	return bestStart + 1, bestHits * 100 / len(pLines)
}
