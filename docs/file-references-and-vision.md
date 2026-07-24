# File references & vision

## `@path` file references

Mention a file or directory anywhere in a prompt to inline its content:

```
> explain @cmd/agent/main.go and how it relates to @internal/agent
> summarize @README.md, focusing on configuration
```

- Files are appended to the message as delimited blocks (**100 KB cap** per file).
- Directories inline their listing.
- A missing path is reported **before anything is sent**.
- `@` references also work in one-shot mode: `agent -p "review @main.go"`, and
  inside [custom slash commands](custom-commands.md).

### Images via `@ref`

An `@ref` pointing at `.png` / `.jpg` / `.jpeg` / `.gif` / `.webp` is attached as
an **image part** instead of being inlined as text — converted to whichever format
the active provider expects (OpenAI-vision data URLs, or Anthropic base64 image
blocks).

## `Ctrl+V` image paste

Press **Ctrl+V** in the input box to paste an image from the system clipboard:

- A clean `[Image #N]` placeholder is inserted at the cursor (never a file path),
  and a `📎 image #N attached` notice appears under the input box.
- On submit the placeholder resolves to a multimodal image part alongside your text.

The image is written to the **system temporary directory** (`os.TempDir()` —
`$TMPDIR` on macOS, `/tmp` on Linux, `%TEMP%` on Windows) under `agent-cli-pastes/`.
It is one-shot scratch: read once when the message is built, then left for the OS
to reclaim — nothing lands in the repository or your home directory. Older in-tree
`.agent/pastes/` images are moved out to temp automatically on startup.

**Requirements:**
- macOS — built-in (`osascript`).
- Linux — needs `wl-clipboard` or `xclip`.
- If the clipboard holds no image, a notice appears and the input is left untouched.

## Vision-capability routing

Image turns are routed by what the active model can actually do:

1. **Vision-capable model** (`gpt-4o`, `claude-*`, `qwen2.5-vl`, `glm-4v`, …,
   detected from the model name) → image parts are sent natively. Mark an
   unrecognized vision model on a custom endpoint with `"vision": true` in its
   profile.

2. **Text-only model with a vision fallback configured** → the images are first
   described by the fallback model, and the description (with visible text
   transcribed verbatim) is fed to your primary model as text. History stays
   image-free, so later turns keep working:

   ```bash
   agent config set vision_provider openai
   agent config set vision_model gpt-4o-mini
   ```
   ```
   > @shot.png what does this error mean?
   🖼 deepseek-chat has no vision — describing image(s) with gpt-4o-mini…
   ```

3. **Neither** → the turn fails **before any API call** with those exact
   instructions — no cryptic provider 400.

Switching mid-session to a text-only model (`/model`, `/provider`) automatically
replaces image parts already in history with placeholders, so the new model isn't
rejected by leftover images.

### Configuration

| Key | Purpose |
|---|---|
| `vision_provider` | Provider used to describe images for a text-only primary model |
| `vision_model` | Model used for that description |
| `"vision": true` (profile field) | Force-mark a custom endpoint's model as vision-capable |
