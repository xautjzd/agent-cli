package provider

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestWireMessagesMultimodal(t *testing.T) {
	msgs := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "look at this", Parts: []Part{
			{Type: "text", Text: "look at this"},
			{Type: "image_url", ImageURL: &ImageURL{URL: "data:image/png;base64,AAAA"}},
		}},
	}
	data, err := json.Marshal(toWireMessages(msgs))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)

	// Plain message keeps string content.
	if !strings.Contains(s, `"content":"sys"`) {
		t.Errorf("plain content not a string: %s", s)
	}
	// Multimodal message becomes a content array in OpenAI vision format.
	if !strings.Contains(s, `"content":[{"type":"text","text":"look at this"}`) ||
		!strings.Contains(s, `"image_url":{"url":"data:image/png;base64,AAAA"}`) {
		t.Errorf("multimodal content wrong: %s", s)
	}
	// The session-only "parts" key must not leak onto the wire.
	if strings.Contains(s, `"parts"`) {
		t.Errorf("parts key leaked to wire: %s", s)
	}
}

// Vision is decided per model where a vendor's line is mixed: MiniMax takes
// image input on M3 only, so the preset cannot claim it wholesale.
func TestVisionIsPerModelForMixedLines(t *testing.T) {
	if !SupportsVision("MiniMax-M3") {
		t.Error("MiniMax-M3 accepts image and video input")
	}
	for _, m := range []string{"MiniMax-M2.7", "MiniMax-M2.5-highspeed", "MiniMax-M2"} {
		if SupportsVision(m) {
			t.Errorf("%s is text-only", m)
		}
	}
}

// The placeholder must not swallow the reason it exists: every request fails
// with the original setup error, so a session that was opened unconfigured
// says the same thing on its first turn that startup would have said.
func TestUnconfiguredCarriesItsError(t *testing.T) {
	setup := errors.New("provider deepseek needs a credential")
	p := Unconfigured(setup)

	if _, err := p.Chat(context.Background(), Request{}); !errors.Is(err, setup) {
		t.Errorf("Chat error = %v, want the setup error", err)
	}
	got, ok := SetupError(p)
	if !ok || !errors.Is(got, setup) {
		t.Errorf("SetupError = %v, %v", got, ok)
	}
	// A working provider is not mistaken for a placeholder.
	real, err := New("custom", Config{APIKey: "k", BaseURL: "https://x/v1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := SetupError(real); ok {
		t.Error("a built provider must not report a setup error")
	}
	// A nil reason still produces a usable error rather than a nil panic.
	if _, err := Unconfigured(nil).Chat(context.Background(), Request{}); err == nil {
		t.Error("Unconfigured(nil) must still fail requests")
	}
}
