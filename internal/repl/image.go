package repl

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/xautjzd/agent-cli/internal/config"
	"github.com/xautjzd/agent-cli/internal/provider"
	"github.com/xautjzd/agent-cli/internal/theme"
)

// Multimodal input support: images pasted with Ctrl+V (or referenced with
// @path) are attached as base64 data-URL parts in the OpenAI vision format.
// Requires a vision-capable model (e.g. gpt-4o); text-only models reject
// content arrays with images.

// mimeTypes maps image extensions to their MIME type for data URLs.
var mimeTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp",
}

// imagePlaceholderRe matches the "[Image #N]" markers left by a Ctrl+V
// paste. N indexes into the paste map so the marker resolves to a real
// image without the path ever appearing in the input.
var imagePlaceholderRe = regexp.MustCompile(`\[Image #(\d+)\]`)

// buildUserMessage assembles the outgoing user message: text plus one
// image_url part per attached image. Images come from two sources — "@path"
// references to files in the project, and "[Image #N]" placeholders from
// clipboard pastes resolved through pastes. wrap post-processes the expanded
// text (plan mode wraps it in planning instructions); pass nil for identity.
func buildUserMessage(input, workDir string, pastes map[int]string, wrap func(string) string) (provider.Message, error) {
	text, images, err := ExpandRefs(input, workDir)
	if err != nil {
		return provider.Message{}, err
	}
	// Resolve pasted-image placeholders. An unknown number is left as
	// plain text — it may be something the user typed by hand.
	for _, match := range imagePlaceholderRe.FindAllStringSubmatch(input, -1) {
		n, _ := strconv.Atoi(match[1])
		if path, ok := pastes[n]; ok {
			images = append(images, path)
		}
	}
	if wrap != nil {
		text = wrap(text)
	}
	msg := provider.Message{Role: provider.RoleUser, Content: text}
	if len(images) == 0 {
		return msg, nil
	}

	msg.Parts = []provider.Part{{Type: "text", Text: text}}
	for _, path := range images {
		data, err := os.ReadFile(path)
		if err != nil {
			return provider.Message{}, fmt.Errorf("read image %s: %w", path, err)
		}
		mime := mimeTypes[strings.ToLower(filepath.Ext(path))]
		if mime == "" {
			mime = "image/png"
		}
		msg.Parts = append(msg.Parts, provider.Part{
			Type: "image_url",
			ImageURL: &provider.ImageURL{
				URL: fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(data)),
			},
		})
	}
	return msg, nil
}

// prepareImageMessage routes a multimodal message by model capability,
// mirroring how Claude Code / codex "just work": their models all have
// vision, so here the capability gap is bridged explicitly.
//
// Key flow: vision-capable primary model → send image parts natively.
// Text-only primary model with a configured vision fallback → a one-off
// call to the vision model describes the images, and the description is
// appended to the text so the primary model (and the whole later history)
// stays image-free. Neither → fail before any API call, with actionable
// guidance instead of a provider 400.
func (r *Repl) prepareImageMessage(ctx context.Context, msg provider.Message) (provider.Message, error) {
	if len(msg.Parts) == 0 || r.Cfg.ModelSupportsVision() {
		return msg, nil
	}
	if r.Cfg.VisionModel == "" {
		return provider.Message{}, fmt.Errorf(
			"model %q cannot read images; either switch to a vision model (/provider openai gpt-4o or /model <vision-model>), "+
				"or configure a fallback that describes images for it: /config set vision_provider openai · /config set vision_model gpt-4o-mini "+
				"(for unrecognized vision models on custom endpoints, mark the profile with \"vision\": true)",
			r.Cfg.Model)
	}

	visionProv, visionModel, err := r.visionClient()
	if err != nil {
		return provider.Message{}, fmt.Errorf("vision fallback: %w", err)
	}
	th := theme.Current()
	fmt.Fprintf(r.Out, "%s\n", th.Paint(th.Muted, fmt.Sprintf("🖼 %s has no vision — describing image(s) with %s…", r.Cfg.Model, visionModel)))

	resp, err := visionProv.Chat(ctx, provider.Request{
		Model: visionModel,
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: "You describe images on behalf of a text-only coding agent. " +
				"Describe the attached image(s) precisely and completely so the agent can act on the description alone. " +
				"Transcribe any visible text verbatim. Answer with the description only."},
			{Role: provider.RoleUser, Content: "User request for context: " + msg.Content, Parts: msg.Parts},
		},
	})
	if err != nil {
		return provider.Message{}, fmt.Errorf("vision fallback (%s): %w", visionModel, err)
	}

	msg.Content += fmt.Sprintf(
		"\n\n--- Image description (by %s; the primary model has no vision) ---\n%s",
		visionModel, resp.Message.Content)
	msg.Parts = nil // keep the conversation history image-free
	return msg, nil
}

// visionClient builds the fallback vision provider. VisionProvider defaults
// to the primary provider when unset. A pre-injected VisionClient (tests,
// future pooling) takes precedence.
func (r *Repl) visionClient() (provider.Provider, string, error) {
	if r.VisionClient != nil {
		return r.VisionClient, r.Cfg.VisionModel, nil
	}
	name := r.Cfg.VisionProvider
	if name == "" {
		name = r.Cfg.Provider
	}
	cfg, err := config.LoadFor(name)
	if err != nil {
		return nil, "", err
	}
	cfg.Model = r.Cfg.VisionModel
	p, err := cfg.BuildProvider()
	if err != nil {
		return nil, "", err
	}
	return p, cfg.Model, nil
}

// readClipboardImage grabs PNG data from the system clipboard. It is a
// variable so tests (and unsupported platforms) can substitute it.
var readClipboardImage = readClipboardImageNative

// readClipboardImageNative shells out to the platform clipboard tool.
//
// Key flow per platform: macOS uses osascript to coerce the clipboard to
// PNG and write it to a temp file; Linux tries wl-paste (Wayland) then
// xclip (X11).
func readClipboardImageNative() ([]byte, error) {
	switch runtime.GOOS {
	case "darwin":
		tmp, err := os.CreateTemp("", "agent-clip-*.png")
		if err != nil {
			return nil, err
		}
		tmp.Close()
		defer os.Remove(tmp.Name())
		script := fmt.Sprintf(
			`set f to (open for access POSIX file %q with write permission)
try
	write (the clipboard as «class PNGf») to f
end try
close access f`, tmp.Name())
		if out, err := exec.Command("osascript", "-e", script).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("osascript: %v: %s", err, out)
		}
		data, err := os.ReadFile(tmp.Name())
		if err != nil || len(data) == 0 {
			return nil, fmt.Errorf("clipboard does not contain an image")
		}
		return data, nil
	case "linux":
		for _, cmd := range [][]string{
			{"wl-paste", "-t", "image/png"},
			{"xclip", "-selection", "clipboard", "-t", "image/png", "-o"},
		} {
			if _, err := exec.LookPath(cmd[0]); err != nil {
				continue
			}
			out, err := exec.Command(cmd[0], cmd[1:]...).Output()
			if err == nil && len(out) > 0 {
				return out, nil
			}
		}
		return nil, fmt.Errorf("no clipboard image (install wl-clipboard or xclip)")
	}
	return nil, fmt.Errorf("clipboard images not supported on %s", runtime.GOOS)
}

// pastesDir is where clipboard images are kept: the system temporary
// directory (os.TempDir resolves it per platform — $TMPDIR on macOS, /tmp
// on Linux, %TEMP% on Windows). Pasted images are one-shot scratch: read
// once when the message is built, then left for the OS to reclaim on its
// normal temp-cleanup schedule. This keeps them out of both the repository
// and the user's home.
func pastesDir() string {
	return filepath.Join(os.TempDir(), "agent-cli-pastes")
}

// MigratePastes moves any clipboard images left in the project's old
// in-tree location to the user-level store, once, so upgrading does not
// leave personal scratch images sitting in a shared repository. It is
// best-effort and never overwrites or deletes data it cannot move.
func MigratePastes(workDir string) {
	legacy := filepath.Join(workDir, ".agent", "pastes")
	entries, err := os.ReadDir(legacy)
	if err != nil || len(entries) == 0 {
		return
	}
	dir := pastesDir()
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		src := filepath.Join(legacy, e.Name())
		// A unique destination in temp; the original name is scratch only.
		f, err := os.CreateTemp(dir, "paste-*.png")
		if err != nil {
			continue
		}
		data, rerr := os.ReadFile(src)
		if rerr != nil {
			f.Close()
			os.Remove(f.Name())
			continue
		}
		if _, werr := f.Write(data); werr != nil {
			f.Close()
			os.Remove(f.Name())
			continue
		}
		f.Close()
		os.Remove(src)
	}
	// Remove only the pastes subdirectory when emptied; the surrounding
	// .agent directory legitimately holds shared skills/memory/config.
	if rest, err := os.ReadDir(legacy); err == nil && len(rest) == 0 {
		os.Remove(legacy)
	}
}

// savePastedImage writes clipboard image data to a fresh file in the paste
// directory and returns its absolute path. os.CreateTemp guarantees a
// unique name even under rapid or concurrent pastes.
func savePastedImage(data []byte) (string, error) {
	dir := pastesDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "paste-*.png")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}
