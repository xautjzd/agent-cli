package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/xautjzd/agent-cli/internal/skill"
)

// UseSkill lets the model load a skill's full instructions on demand.
// Skill metadata (name + description) is listed in the system prompt; the
// body is only pulled into context when actually needed, mirroring how
// Claude Code keeps skills cheap until invoked.
type UseSkill struct {
	Repo skill.Repository
}

func (t *UseSkill) Name() string { return "use_skill" }

func (t *UseSkill) Description() string {
	return "Load the full instructions of an installed skill by name. " +
		"Invoke when the current task matches a skill's description, then follow the returned instructions."
}

func (t *UseSkill) Schema() json.RawMessage {
	return mustSchema(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "The skill name as listed in the system prompt"}
		},
		"required": ["name"]
	}`)
}

func (t *UseSkill) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	s, err := t.Repo.Load(args.Name)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Skill %q loaded (directory: %s). Follow these instructions:\n\n%s",
		s.Name, s.Dir, s.Body), nil
}
