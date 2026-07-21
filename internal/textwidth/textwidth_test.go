package textwidth

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestWidthCountsColumnsNotRunes(t *testing.T) {
	cases := map[string]int{
		"hello": 5,
		"你好":    4, // two columns per CJK character
		"a你b":   4,
		"":      0,
		"世界杯决赛": 10,
	}
	for in, want := range cases {
		if got := Width(in); got != want {
			t.Errorf("Width(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestTruncateNeverSplitsARune(t *testing.T) {
	// The original bug: byte slicing cut UTF-8 sequences in half, rendering
	// as replacement characters in the session list.
	long := "图里说的是什么内容请详细描述一下"
	for max := 1; max <= Width(long)+2; max++ {
		got := Truncate(long, max)
		if !utf8.ValidString(got) {
			t.Fatalf("Truncate(%q, %d) produced invalid UTF-8: %q", long, max, got)
		}
		if strings.ContainsRune(got, '�') {
			t.Fatalf("Truncate(%q, %d) produced a replacement char: %q", long, max, got)
		}
		if w := Width(got); w > max {
			t.Errorf("Truncate(%q, %d) width = %d, exceeds budget", long, max, w)
		}
	}
}

func TestTruncateMarksElisionOnly(t *testing.T) {
	if got := Truncate("hello", 10); got != "hello" {
		t.Errorf("short string must be untouched, got %q", got)
	}
	got := Truncate("hello world", 8)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated string should end with an ellipsis: %q", got)
	}
	if Width(got) > 8 {
		t.Errorf("Truncate width = %d, want <= 8", Width(got))
	}
	if Truncate("anything", 0) != "" {
		t.Error("zero budget should yield an empty string")
	}
}

func TestPadAlignsMixedScripts(t *testing.T) {
	// The alignment bug: %-Ns pads by rune count, so a CJK title consumed
	// twice the columns and pushed the next column right.
	ascii := Pad("hello", 12)
	cjk := Pad("你是什么模型", 12) // 6 chars, 12 columns — exactly fills
	if Width(ascii) != 12 || Width(cjk) != 12 {
		t.Errorf("padded widths = %d and %d, want both 12", Width(ascii), Width(cjk))
	}

	// A field always occupies exactly its width, even when the content is
	// too long — otherwise one long row shifts every column after it.
	overflow := Pad("这是一个非常非常长的标题需要被截断", 12)
	if Width(overflow) != 12 {
		t.Errorf("overflowing field width = %d, want 12", Width(overflow))
	}
}

func TestPadLeft(t *testing.T) {
	got := PadLeft("42", 6)
	if got != "    42" {
		t.Errorf("PadLeft = %q", got)
	}
	if Width(PadLeft("你好", 6)) != 6 {
		t.Error("right-aligned CJK field has the wrong width")
	}
}

func TestWrapRespectsWidthAndWords(t *testing.T) {
	lines := Wrap("the quick brown fox jumps over the lazy dog", 12)
	for i, l := range lines {
		if Width(l) > 12 {
			t.Errorf("line %d width = %d, exceeds 12: %q", i, Width(l), l)
		}
		if strings.HasPrefix(l, " ") || strings.HasSuffix(l, " ") {
			t.Errorf("line %d has stray padding: %q", i, l)
		}
	}
	// Nothing may be lost or duplicated by wrapping.
	if joined := strings.Join(lines, " "); joined != "the quick brown fox jumps over the lazy dog" {
		t.Errorf("wrap changed the text: %q", joined)
	}
}

func TestWrapHardSplitsUnbreakableWords(t *testing.T) {
	// A long URL has no spaces; it must be split rather than overflow.
	url := "https://ws-mkh2yc3hngbq7zqg.ap-southeast-1.maas.aliyuncs.com/apps/anthropic"
	lines := Wrap(url, 20)
	if len(lines) < 2 {
		t.Fatalf("long token was not split: %v", lines)
	}
	for i, l := range lines {
		if Width(l) > 20 {
			t.Errorf("line %d width = %d, exceeds 20: %q", i, Width(l), l)
		}
	}
	if rejoined := strings.Join(lines, ""); rejoined != url {
		t.Errorf("hard split lost characters:\n got %q\nwant %q", rejoined, url)
	}
}

func TestWrapCJK(t *testing.T) {
	// Two columns per character, so a 10-column line holds five of them.
	lines := Wrap("你好世界你好世界你好世界", 10)
	for i, l := range lines {
		if Width(l) > 10 {
			t.Errorf("line %d width = %d, exceeds 10: %q", i, Width(l), l)
		}
		if strings.ContainsRune(l, '�') {
			t.Errorf("line %d has a replacement character: %q", i, l)
		}
	}
}

func TestWriteListAlignsHangingIndent(t *testing.T) {
	var buf strings.Builder
	WriteList(&buf, [][2]string{
		{"bash", "Execute a shell command in the project directory and return its combined output."},
		{"read_file", "Read a file."},
		{"a-very-long-tool-name-that-exceeds-the-cap", "Short."},
	}, 60, 3)

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) < 4 {
		t.Fatalf("expected wrapped output, got:\n%s", buf.String())
	}
	for i, l := range lines {
		if Width(l) > 60 {
			t.Errorf("line %d width = %d, exceeds the available width: %q", i, Width(l), l)
		}
	}

	// Every description — first line or continuation — starts at the same
	// column; that is what was broken when long text wrapped to column 0.
	descCol := strings.Index(lines[0], "Execute")
	for i, l := range lines {
		var col int
		if trimmed := strings.TrimLeft(l, " "); trimmed != l {
			col = len(l) - len(trimmed)
			if col != descCol {
				t.Errorf("continuation line %d indented to %d, want %d: %q", i, col, descCol, l)
			}
		}
	}

	// An over-long name is truncated rather than pushing its description.
	if !strings.Contains(buf.String(), "…") {
		t.Errorf("over-long name should be elided:\n%s", buf.String())
	}
}

func TestWriteListCapsDescriptionLines(t *testing.T) {
	var buf strings.Builder
	long := strings.Repeat("word ", 200)
	WriteList(&buf, [][2]string{{"name", long}}, 60, 2)

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("description not capped at 2 lines, got %d:\n%s", len(lines), buf.String())
	}
	if !strings.HasSuffix(lines[1], "…") {
		t.Errorf("elision not marked: %q", lines[1])
	}
}

func TestWriteListEmpty(t *testing.T) {
	var buf strings.Builder
	WriteList(&buf, nil, 60, 2)
	if buf.Len() != 0 {
		t.Errorf("empty list should render nothing, got %q", buf.String())
	}
}
