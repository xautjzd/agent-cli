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
