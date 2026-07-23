package lsp

import (
	"encoding/json"
	"testing"
)

func TestUTF16Len(t *testing.T) {
	cases := map[string]int{
		"":             0,
		"abc":          3,
		"héllo":        5, // é is one UTF-16 unit
		"a\U0001F600b": 4, // emoji is a surrogate pair → 2 units
	}
	for s, want := range cases {
		if got := utf16Len(s); got != want {
			t.Errorf("utf16Len(%q) = %d, want %d", s, got, want)
		}
	}
}

func TestLocateSymbol(t *testing.T) {
	content := "package main\n\nfunc Hello() {}\nvar x = Hello\n"

	// On the given line, the character is the UTF-16 offset of the identifier.
	pos, err := locateSymbol(content, "Hello", 3)
	if err != nil {
		t.Fatal(err)
	}
	if pos.Line != 2 || pos.Character != 5 {
		t.Errorf("pos = %+v, want line 2 char 5", pos)
	}

	// With no line hint, the first occurrence is used.
	pos, err = locateSymbol(content, "Hello", 0)
	if err != nil {
		t.Fatal(err)
	}
	if pos.Line != 2 {
		t.Errorf("first-occurrence line = %d, want 2", pos.Line)
	}

	// A symbol absent from the named line is an error.
	if _, err := locateSymbol(content, "Hello", 1); err == nil {
		t.Error("expected error for symbol missing on the line")
	}
}

func TestURIRoundTrip(t *testing.T) {
	path := "/Users/jz/pro ject/main.go" // space must be encoded
	uri := pathToURI(path)
	if uri == "file:///Users/jz/pro ject/main.go" {
		t.Errorf("uri not encoded: %s", uri)
	}
	if got := uriToPath(uri); got != path {
		t.Errorf("round trip = %q, want %q", got, path)
	}
}

func TestMergeDefsInheritsAndAppends(t *testing.T) {
	defaults := []ServerDef{
		{Lang: "go", Command: "gopls", Extensions: []string{".go"}},
	}
	overrides := []ServerDef{
		{Lang: "go", Command: "gopls-custom"},                       // extensions inherited
		{Lang: "zig", Command: "zls", Extensions: []string{".zig"}}, // new language
	}
	merged := mergeDefs(defaults, overrides)
	byLang := map[string]ServerDef{}
	for _, d := range merged {
		byLang[d.Lang] = d
	}
	if g := byLang["go"]; g.Command != "gopls-custom" || len(g.Extensions) != 1 || g.Extensions[0] != ".go" {
		t.Errorf("go override did not inherit extensions: %+v", g)
	}
	if _, ok := byLang["zig"]; !ok {
		t.Errorf("new language not appended: %+v", merged)
	}
}

func TestDefForPathSkipsDisabled(t *testing.T) {
	m := NewManager("/root", []ServerDef{{Lang: "go", Disabled: true}})
	if _, ok := m.defForPath("main.go"); ok {
		t.Error("disabled language must not route")
	}
}

func TestFlattenHover(t *testing.T) {
	// MarkupContent object.
	if got := flattenHover(json.RawMessage(`{"kind":"markdown","value":"func Hello()"}`)); got != "func Hello()" {
		t.Errorf("markup = %q", got)
	}
	// Plain string.
	if got := flattenHover(json.RawMessage(`"just text"`)); got != "just text" {
		t.Errorf("string = %q", got)
	}
	// Array of marked strings.
	got := flattenHover(json.RawMessage(`[{"value":"a"},{"value":"b"}]`))
	if got != "a\n\nb" {
		t.Errorf("array = %q", got)
	}
}

func TestParseLocations(t *testing.T) {
	// Single object.
	one := parseLocations(json.RawMessage(`{"uri":"file:///a.go","range":{"start":{"line":1,"character":2},"end":{"line":1,"character":7}}}`))
	if len(one) != 1 || one[0].Range.Start.Line != 1 {
		t.Errorf("single = %+v", one)
	}
	// Array.
	arr := parseLocations(json.RawMessage(`[{"uri":"file:///a.go","range":{"start":{"line":0,"character":0},"end":{"line":0,"character":1}}}]`))
	if len(arr) != 1 {
		t.Errorf("array = %+v", arr)
	}
	// Null.
	if got := parseLocations(json.RawMessage(`null`)); got != nil {
		t.Errorf("null = %+v", got)
	}
}

func TestFormatLocations(t *testing.T) {
	locs := []Location{
		{URI: "file:///root/b.go", Range: Range{Start: Position{Line: 4, Character: 1}}},
		{URI: "file:///root/a.go", Range: Range{Start: Position{Line: 0, Character: 0}}},
		{URI: "file:///root/a.go", Range: Range{Start: Position{Line: 0, Character: 0}}}, // dup
	}
	got := formatLocations("/root", locs)
	// Sorted, de-duped, 1-based.
	want := "a.go:1:1\nb.go:5:2"
	if got != want {
		t.Errorf("formatLocations =\n%q\nwant\n%q", got, want)
	}
}
