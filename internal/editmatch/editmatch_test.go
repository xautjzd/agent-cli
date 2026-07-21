package editmatch

import (
	"strings"
	"testing"
)

func TestExactMatch(t *testing.T) {
	content := "line one\nline two\nline three\n"
	res, err := Replace(content, "line two", "LINE 2", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Strategy != strategyExact || res.Fuzzy() {
		t.Errorf("expected exact, got %q (fuzzy=%v)", res.Strategy, res.Fuzzy())
	}
	if res.Updated != "line one\nLINE 2\nline three\n" {
		t.Errorf("wrong result: %q", res.Updated)
	}
}

func TestExactAmbiguous(t *testing.T) {
	content := "x = 1\nx = 1\n"
	if _, err := Replace(content, "x = 1", "x = 2", false); err == nil {
		t.Fatal("expected ambiguity error")
	}
	// replace_all resolves it.
	res, err := Replace(content, "x = 1", "x = 2", true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Count != 2 || res.Updated != "x = 2\nx = 2\n" {
		t.Errorf("replace_all wrong: count=%d %q", res.Count, res.Updated)
	}
}

func TestLineTrimTolerance(t *testing.T) {
	// A multi-line block where the file has trailing whitespace and tab indent
	// the model's pattern omits. A single line would be caught by the exact
	// substring tier; a multi-line block with differing internal indentation
	// is not a byte substring, so the line-trim tier must handle it.
	content := "func f() {\n\ta := 1 \n\tb := 2\n}\n" // trailing space, tab indent
	pattern := "a := 1\nb := 2"                       // no indent, no trailing space
	res, err := Replace(content, pattern, "a := 10\nb := 20", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Strategy != strategyLineTrim {
		t.Errorf("expected line-trim, got %q", res.Strategy)
	}
	// The replacement is re-indented to the target's tab indentation.
	if res.Updated != "func f() {\n\ta := 10\n\tb := 20\n}\n" {
		t.Errorf("reindent wrong: %q", res.Updated)
	}
}

func TestReindentMultiLineBlock(t *testing.T) {
	// Model supplies a 2-line block at zero indent; the file has it indented
	// two spaces. The whole replacement should be shifted to match.
	content := "if x {\n  a()\n  b()\n}\n"
	pattern := "a()\nb()"
	replacement := "a()\nc()\nb()"
	res, err := Replace(content, pattern, replacement, false)
	if err != nil {
		t.Fatal(err)
	}
	want := "if x {\n  a()\n  c()\n  b()\n}\n"
	if res.Updated != want {
		t.Errorf("multi-line reindent wrong:\n got %q\nwant %q", res.Updated, want)
	}
}

func TestReindentDedent(t *testing.T) {
	// Model supplies an over-indented pattern; file is at a shallower indent.
	content := "def f():\n    return 1\n"
	pattern := "        return 1" // 8 spaces
	res, err := Replace(content, pattern, "        return 2", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated != "def f():\n    return 2\n" {
		t.Errorf("dedent reindent wrong: %q", res.Updated)
	}
}

func TestWSCollapseTier(t *testing.T) {
	// Internal spacing differs (multiple spaces around '='); only the collapse
	// tier can match.
	content := "config  =    {\n    key: 1,\n}\n"
	pattern := "config = {"
	res, err := Replace(content, pattern, "config = MAP{", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Strategy != strategyCollapse {
		t.Errorf("expected ws-collapse, got %q", res.Strategy)
	}
	if !strings.Contains(res.Updated, "config = MAP{") {
		t.Errorf("collapse replacement wrong: %q", res.Updated)
	}
}

func TestFuzzyAmbiguityStillRejected(t *testing.T) {
	// Two whitespace-variant matches must be rejected without replace_all,
	// so a fuzzy edit never silently hits the wrong one.
	content := "  return 1\n\treturn 1 \n"
	if _, err := Replace(content, "return 1", "return 2", false); err == nil {
		t.Fatal("expected ambiguity across fuzzy matches")
	}
	if !strings.Contains(mustErr(t, content, "return 1", "return 2"), "2 locations") {
		t.Error("ambiguity error should report the count")
	}
}

func TestNotFoundNearestRegion(t *testing.T) {
	content := "alpha\nbeta\ngamma\ndelta\nepsilon\n"
	// Pattern shares most lines with the beta..delta region but one line is
	// wrong, so no tier matches; the error should point near line 2.
	_, err := Replace(content, "beta\nWRONG\ndelta", "x", false)
	if err == nil {
		t.Fatal("expected not-found")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("expected nearest-region hint at line 2, got: %v", err)
	}
}

func TestNotFoundNoSimilarity(t *testing.T) {
	content := "totally\nunrelated\n"
	_, err := Replace(content, "nothing like this at all", "x", false)
	if err == nil || !strings.Contains(err.Error(), "no similar text") {
		t.Errorf("expected a no-similarity message, got: %v", err)
	}
}

func TestEmptyPattern(t *testing.T) {
	if _, err := Replace("abc", "", "x", false); err == nil {
		t.Error("empty pattern must error")
	}
}

func TestExactPreferredOverFuzzy(t *testing.T) {
	// When an exact match exists, it must win even though fuzzy would also
	// match — exact is unambiguous and reindent-free.
	content := "  x\nx\n"
	res, err := Replace(content, "x", "y", false)
	if err == nil {
		// "x" appears exactly twice as a substring → ambiguous exact, which is
		// the correct, safe behavior (not a fuzzy fallthrough).
		t.Fatalf("expected exact-ambiguity, got strategy %q", res.Strategy)
	}
	if !strings.Contains(err.Error(), "locations") {
		t.Errorf("expected ambiguity, got: %v", err)
	}
}

func TestBlankLinesInReplacementUnindented(t *testing.T) {
	// Reindent must leave blank lines empty rather than filling them with the
	// indent prefix. Use a multi-line block so the line-trim/reindent tier
	// runs (a single token would match exactly and skip reindent).
	content := "func f() {\n\tx()\n\told()\n}\n"
	res, err := Replace(content, "x()\nold()", "a()\n\nb()", false)
	if err != nil {
		t.Fatal(err)
	}
	// Non-blank lines gain the tab indent; the blank middle line stays blank.
	if !strings.Contains(res.Updated, "\ta()\n\n\tb()") {
		t.Errorf("blank line handling wrong: %q", res.Updated)
	}
}

func mustErr(t *testing.T, content, pat, repl string) string {
	t.Helper()
	_, err := Replace(content, pat, repl, false)
	if err == nil {
		t.Fatal("expected error")
	}
	return err.Error()
}
