package repl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xautjzd/agent-cli/internal/config"
	"github.com/xautjzd/agent-cli/internal/provider"
)

// pngHeader is a minimal PNG signature — enough for file-shape assertions.
var pngHeader = []byte("\x89PNG\r\n\x1a\nfake-image-data")

func TestExpandRefsSeparatesImages(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "shot.png"), pngHeader, 0o644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("text body"), 0o644)

	text, images, err := ExpandRefs("compare @shot.png with @notes.txt", dir)
	if err != nil {
		t.Fatal(err)
	}
	// The text file is inlined; the image is not.
	if !strings.Contains(text, "text body") {
		t.Errorf("text ref not inlined:\n%s", text)
	}
	if strings.Contains(text, "shot.png ---") {
		t.Errorf("image wrongly inlined as text:\n%s", text)
	}
	// The mention itself stays visible.
	if !strings.Contains(text, "@shot.png") {
		t.Errorf("image mention removed:\n%s", text)
	}
	if len(images) != 1 || filepath.Base(images[0]) != "shot.png" {
		t.Errorf("images = %v", images)
	}

	// A missing image is a hard error.
	if _, _, err := ExpandRefs("see @missing.png", dir); err == nil {
		t.Error("expected error for missing image")
	}
}

func TestBuildUserMessageWithImage(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "ui.jpg"), []byte("jpegdata"), 0o644)

	msg, err := buildUserMessage("what is wrong in @ui.jpg", dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.Parts) != 2 {
		t.Fatalf("parts = %d, want text + image", len(msg.Parts))
	}
	if msg.Parts[0].Type != "text" || !strings.Contains(msg.Parts[0].Text, "@ui.jpg") {
		t.Errorf("text part wrong: %+v", msg.Parts[0])
	}
	img := msg.Parts[1]
	if img.Type != "image_url" || !strings.HasPrefix(img.ImageURL.URL, "data:image/jpeg;base64,") {
		t.Errorf("image part wrong: %+v", img)
	}

	// Text-only input produces a plain message (no parts array on wire).
	plain, err := buildUserMessage("just text", dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.Parts) != 0 {
		t.Errorf("unexpected parts for text-only input: %+v", plain.Parts)
	}
}

func TestMultimodalMessageReachesProvider(t *testing.T) {
	// A vision-capable model receives image parts natively.
	r, stub, _ := newTestRepl(t, "")
	r.Cfg.Model = "gpt-4o"
	r.Agent.SetModel("gpt-4o")
	os.WriteFile(filepath.Join(r.WorkDir, "bug.png"), pngHeader, 0o644)

	if err := r.runPrompt(context.Background(), "explain @bug.png"); err != nil {
		t.Fatal(err)
	}
	msgs := stub.last.Messages
	// The user message (before the assistant reply) must carry the parts.
	user := msgs[len(msgs)-1]
	if len(user.Parts) != 2 || user.Parts[1].Type != "image_url" {
		t.Errorf("provider did not receive image parts: %+v", user.Parts)
	}
}

func TestImageRejectedWithoutVisionOrFallback(t *testing.T) {
	// A text-only model with no vision fallback fails before any API call,
	// with actionable guidance.
	r, stub, _ := newTestRepl(t, "")
	os.WriteFile(filepath.Join(r.WorkDir, "bug.png"), pngHeader, 0o644)

	err := r.runPrompt(context.Background(), "explain @bug.png")
	if err == nil || !strings.Contains(err.Error(), "cannot read images") {
		t.Fatalf("expected pre-flight error, got %v", err)
	}
	if !strings.Contains(err.Error(), "vision_model") {
		t.Errorf("error should mention the fallback option: %v", err)
	}
	if len(stub.requests) != 0 {
		t.Error("no provider request may be sent when images are unroutable")
	}
}

func TestVisionProfileFlagEnablesNativeImages(t *testing.T) {
	// A named profile marked "vision": true sends parts natively even when
	// the model name is unrecognized.
	r, stub, _ := newTestRepl(t, "")
	r.Cfg.Provider = "myvision"
	r.Cfg.Providers = map[string]config.ProviderConfig{
		"myvision": {BaseURL: "http://x", Vision: true},
	}
	os.WriteFile(filepath.Join(r.WorkDir, "bug.png"), pngHeader, 0o644)

	if err := r.runPrompt(context.Background(), "explain @bug.png"); err != nil {
		t.Fatal(err)
	}
	user := stub.last.Messages[len(stub.last.Messages)-1]
	if len(user.Parts) != 2 {
		t.Errorf("profile vision flag not honored: %+v", user.Parts)
	}
}

func TestStripImagePartsOnModelSwitch(t *testing.T) {
	r, stub, out := newTestRepl(t, "")
	r.Cfg.Model = "gpt-4o"
	r.Agent.SetModel("gpt-4o")
	os.WriteFile(filepath.Join(r.WorkDir, "bug.png"), pngHeader, 0o644)
	if err := r.runPrompt(context.Background(), "explain @bug.png"); err != nil {
		t.Fatal(err)
	}

	// Switching to a text-only model must scrub image parts from history so
	// later requests are not rejected.
	if err := r.dispatch(context.Background(), "/model deepseek-chat"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "image message(s) in history replaced") {
		t.Errorf("missing strip notice: %s", out.String())
	}
	if err := r.runPrompt(context.Background(), "continue"); err != nil {
		t.Fatal(err)
	}
	for _, m := range stub.last.Messages {
		if len(m.Parts) > 0 {
			t.Fatalf("image parts still in history after switch: %+v", m.Parts)
		}
		if m.Role == provider.RoleUser && strings.Contains(m.Content, "explain @bug.png") &&
			!strings.Contains(m.Content, "[image omitted") {
			t.Error("placeholder note missing from stripped message")
		}
	}
}

// visionStub answers any vision request with a fixed description and
// records what it was asked.
type visionStub struct{ last provider.Request }

func (v *visionStub) Name() string { return "vision-stub" }
func (v *visionStub) Chat(_ context.Context, req provider.Request) (*provider.Response, error) {
	v.last = req
	return &provider.Response{Message: provider.Message{
		Role: provider.RoleAssistant, Content: "A red error dialog reading OUT OF MEMORY.",
	}}, nil
}

func TestVisionFallbackDescribesImages(t *testing.T) {
	// Text-only primary model + configured vision fallback: the image is
	// described by the vision model and the primary model receives text
	// only — history stays image-free.
	r, stub, out := newTestRepl(t, "")
	vs := &visionStub{}
	r.VisionClient = vs
	r.Cfg.VisionModel = "fake-vl"
	os.WriteFile(filepath.Join(r.WorkDir, "err.png"), pngHeader, 0o644)

	if err := r.runPrompt(context.Background(), "what does @err.png show?"); err != nil {
		t.Fatal(err)
	}
	// The vision stub received the actual image parts.
	vuser := vs.last.Messages[len(vs.last.Messages)-1]
	if len(vuser.Parts) != 2 || vuser.Parts[1].Type != "image_url" {
		t.Errorf("vision model did not receive parts: %+v", vuser.Parts)
	}
	// The primary model received the description as text, no parts.
	puser := stub.last.Messages[len(stub.last.Messages)-1]
	if len(puser.Parts) != 0 {
		t.Errorf("primary model must not receive parts: %+v", puser.Parts)
	}
	if !strings.Contains(puser.Content, "OUT OF MEMORY") ||
		!strings.Contains(puser.Content, "Image description (by fake-vl") {
		t.Errorf("description not injected:\n%s", puser.Content)
	}
	if !strings.Contains(out.String(), "describing image(s) with fake-vl") {
		t.Errorf("missing fallback notice: %s", out.String())
	}
}

func TestCtrlVPastesImage(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	orig := readClipboardImage
	defer func() { readClipboardImage = orig }()
	readClipboardImage = func() ([]byte, error) { return pngHeader, nil }

	m := newEditorModel(r, "> ")
	typeText(m, "look at ")
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})

	// The input shows a clean placeholder, never a file path.
	value := m.input.Value()
	if value != "look at [Image #1] " {
		t.Errorf("paste did not insert a clean placeholder: %q", value)
	}
	if strings.Contains(value, "/") || strings.Contains(value, "@") || strings.Contains(value, ".png") {
		t.Errorf("a path leaked into the input: %q", value)
	}
	if !strings.Contains(m.status, "image #1") {
		t.Errorf("status = %q", m.status)
	}
	// The file is stored out of the project, under the agent home.
	path := r.imagePastes[1]
	if path == "" {
		t.Fatal("paste was not recorded in the map")
	}
	if strings.HasPrefix(path, r.WorkDir) {
		t.Errorf("pasted image stored inside the project: %s", path)
	}
	// Stored in the system temp directory, so the OS reclaims it.
	if !strings.HasPrefix(path, os.TempDir()) {
		t.Errorf("pasted image not under the system temp dir: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != string(pngHeader) {
		t.Errorf("pasted file wrong: %v", err)
	}

	// A second paste gets the next number.
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	if !strings.Contains(m.input.Value(), "[Image #2]") {
		t.Errorf("second paste did not increment: %q", m.input.Value())
	}

	// On submit, the placeholder resolves to an image part while staying
	// visible in the text.
	msg, err := buildUserMessage("look at [Image #1] please", r.WorkDir, r.imagePastes, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg.Content, "[Image #1]") {
		t.Errorf("placeholder should remain in the text: %q", msg.Content)
	}
	if len(msg.Parts) != 2 || msg.Parts[1].Type != "image_url" {
		t.Errorf("placeholder did not become an image part: %+v", msg.Parts)
	}
	// An unknown number is left as plain text, not an error.
	plain, err := buildUserMessage("what about [Image #9]", r.WorkDir, r.imagePastes, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.Parts) != 0 {
		t.Errorf("unknown placeholder should not attach anything: %+v", plain.Parts)
	}

	// Clipboard failure surfaces as a status notice, not a crash.
	readClipboardImage = func() ([]byte, error) { return nil, fmt.Errorf("no image on clipboard") }
	m2 := newEditorModel(r, "> ")
	m2.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	if !strings.Contains(m2.status, "no image on clipboard") {
		t.Errorf("error status missing: %q", m2.status)
	}
	if m2.input.Value() != "" {
		t.Error("failed paste must not modify input")
	}
}

func TestMigratePastesOutOfProject(t *testing.T) {
	project := t.TempDir()

	// An older version left a pasted image and shared config in .agent.
	legacy := filepath.Join(project, ".agent", "pastes")
	os.MkdirAll(legacy, 0o755)
	os.WriteFile(filepath.Join(legacy, "old.png"), pngHeader, 0o644)
	os.MkdirAll(filepath.Join(project, ".agent", "skills"), 0o755) // must survive

	MigratePastes(project)

	// The image moved out of the working tree.
	if _, err := os.Stat(filepath.Join(legacy, "old.png")); !os.IsNotExist(err) {
		t.Error("paste not migrated out of the project")
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Error("emptied pastes directory should be removed")
	}
	// Shared project data is untouched.
	if _, err := os.Stat(filepath.Join(project, ".agent", "skills")); err != nil {
		t.Error("migration removed shared project data")
	}
	// The file now lives in the system temp paste dir (renamed uniquely).
	entries, err := os.ReadDir(pastesDir())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		data, rerr := os.ReadFile(filepath.Join(pastesDir(), e.Name()))
		if rerr == nil && string(data) == string(pngHeader) {
			found = true
		}
	}
	if !found {
		t.Error("migrated paste not found in the temp dir")
	}
}
