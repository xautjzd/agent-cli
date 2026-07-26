package provider

import (
	"encoding/json"
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
