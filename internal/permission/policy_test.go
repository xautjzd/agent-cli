package permission

import (
	"encoding/json"
	"testing"
)

func bash(cmd string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"command": cmd})
	return b
}
func write(path string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"path": path, "content": "x"})
	return b
}

func TestPolicyDefaultsFromClassifier(t *testing.T) {
	p, _ := NewPolicy(PostureStandard, nil)

	// Safe command → allow, not dangerous.
	d := p.Evaluate("bash", bash("go test ./..."), "/proj")
	if d.Action != ActionAllow || d.Dangerous {
		t.Errorf("safe command: got %+v", d)
	}
	// Dangerous command → ask.
	d = p.Evaluate("bash", bash("rm -rf /"), "/proj")
	if d.Action != ActionAsk || !d.Dangerous {
		t.Errorf("dangerous command: got %+v", d)
	}
}

func TestPolicyDenyRuleBlocks(t *testing.T) {
	p, errs := NewPolicy(PostureStandard, []Rule{
		{Tool: "bash", Command: `\bgit\s+push\b`, Action: ActionDeny},
	})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	d := p.Evaluate("bash", bash("git push origin main"), "/proj")
	if d.Action != ActionDeny {
		t.Errorf("deny rule should block git push: %+v", d)
	}
	// A non-matching command is unaffected.
	if d := p.Evaluate("bash", bash("ls"), "/proj"); d.Action != ActionAllow {
		t.Errorf("non-matching command should be allowed: %+v", d)
	}
}

func TestPolicyAllowRuleSkipsPrompt(t *testing.T) {
	// Allow all edits under src/, even though writes are otherwise dangerous
	// when outside the project (here they're inside, so classifier is safe;
	// use an outside path to prove the rule overrides the danger).
	p, _ := NewPolicy(PostureStandard, []Rule{
		{Tool: "write_file", Path: "**", Action: ActionAllow},
	})
	d := p.Evaluate("write_file", write("/etc/hosts"), "/proj")
	if d.Action != ActionAllow {
		t.Errorf("allow rule should override danger: %+v", d)
	}
	if !d.Dangerous {
		t.Error("Dangerous should still reflect the classifier (true), even when allowed")
	}
}

func TestPolicyPathGlob(t *testing.T) {
	p, _ := NewPolicy(PostureStandard, []Rule{
		{Tool: "edit_file", Path: "src/**", Action: ActionDeny},
	})
	// Inside src/ → deny.
	if d := p.Evaluate("edit_file", write("/proj/src/app/main.go"), "/proj"); d.Action != ActionDeny {
		t.Errorf("src/** should match nested file: %+v", d)
	}
	// Outside src/ → not matched by the rule (falls to classifier: in-project, safe).
	if d := p.Evaluate("edit_file", write("/proj/docs/readme.md"), "/proj"); d.Action == ActionDeny {
		t.Errorf("docs file should not match src/**: %+v", d)
	}
}

func TestPolicyFirstMatchWins(t *testing.T) {
	p, _ := NewPolicy(PostureStandard, []Rule{
		{Tool: "bash", Command: "rm", Action: ActionAllow},
		{Tool: "bash", Command: "rm", Action: ActionDeny},
	})
	if d := p.Evaluate("bash", bash("rm x"), "/proj"); d.Action != ActionAllow {
		t.Errorf("first matching rule should win: %+v", d)
	}
}

func TestPolicyPrependOverrides(t *testing.T) {
	p, _ := NewPolicy(PostureStandard, []Rule{
		{Tool: "bash", Command: "rm", Action: ActionAsk},
	})
	// A session "always allow" prepended rule takes precedence.
	if err := p.Prepend(Rule{Tool: "bash", Command: `^\s*rm\b`, Action: ActionAllow}); err != nil {
		t.Fatal(err)
	}
	if d := p.Evaluate("bash", bash("rm x"), "/proj"); d.Action != ActionAllow {
		t.Errorf("prepended allow should win: %+v", d)
	}
}

func TestPolicyInvalidRegexReported(t *testing.T) {
	_, errs := NewPolicy(PostureStandard, []Rule{{Tool: "bash", Command: "(", Action: ActionDeny}})
	if len(errs) == 0 {
		t.Error("invalid regex should be reported")
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		glob, path string
		want       bool
	}{
		{"src/**", "src/a/b.go", true},
		{"src/**", "src/x.go", true},
		{"src/**", "lib/x.go", false},
		{"*.go", "main.go", true},
		{"*.go", "sub/main.go", false},
		{"**/*.go", "a/b/c.go", true},
		{"config.json", "config.json", true},
	}
	for _, c := range cases {
		if got := globMatch(c.glob, c.path); got != c.want {
			t.Errorf("globMatch(%q,%q) = %v, want %v", c.glob, c.path, got, c.want)
		}
	}
}

func TestBaseProgram(t *testing.T) {
	if got := BaseProgram("npm publish --tag latest"); got != "npm" {
		t.Errorf("BaseProgram = %q, want npm", got)
	}
	if got := BaseProgram("/usr/bin/rm -rf x"); got != "rm" {
		t.Errorf("BaseProgram = %q, want rm", got)
	}
}
