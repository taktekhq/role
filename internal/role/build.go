package role

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// StateRoot is $XDG_STATE_HOME/role, default ~/.local/state/role.
func StateRoot() string {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "role")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "role")
}

// Result describes one built role.
type Result struct {
	Role   string
	Bytes  int
	Skills int
	Over   bool
	Budget int
}

// System assembles the system prompt: each start file, then the role prompt.
func (b *Brain) System(r *Role) ([]byte, error) {
	var buf bytes.Buffer
	add := func(label, path string) error {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("role %s: %w", r.Name, err)
		}
		fmt.Fprintf(&buf, "<!-- from: %s -->\n%s", label, data)
		if len(data) == 0 || data[len(data)-1] != '\n' {
			buf.WriteByte('\n')
		}
		buf.WriteByte('\n')
		return nil
	}
	for _, s := range b.Config.Start {
		if err := add(s, filepath.Join(b.Dir, s)); err != nil {
			return nil, err
		}
	}
	if err := add(filepath.Join("roles", r.Name, "prompt.md"), filepath.Join(r.Dir, "prompt.md")); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Build writes <out>/<role>/system.md and <out>/<role>/plugin/.
func (b *Brain) Build(r *Role, out string) (Result, error) {
	res := Result{Role: r.Name, Budget: b.Config.BudgetBytes}
	for _, f := range []string{"settings.json", "mcp.json"} {
		p := filepath.Join(r.Dir, f)
		if _, err := os.Stat(p); err == nil {
			if err := validJSONFile(p); err != nil {
				return res, fmt.Errorf("role %s: %w", r.Name, err)
			}
		}
	}
	sys, err := b.System(r)
	if err != nil {
		return res, err
	}
	links := map[string]string{}
	for _, s := range r.Skills {
		d, err := b.ResolveSkill(r, s)
		if err != nil {
			return res, err
		}
		links[s] = d
	}
	dir := filepath.Join(out, r.Name)
	plug := filepath.Join(dir, "plugin")
	if err := os.RemoveAll(plug); err != nil {
		return res, err
	}
	if err := os.MkdirAll(filepath.Join(plug, ".claude-plugin"), 0o755); err != nil {
		return res, err
	}
	if err := os.MkdirAll(filepath.Join(plug, "skills"), 0o755); err != nil {
		return res, err
	}
	manifest, _ := json.MarshalIndent(map[string]string{
		"name": "role-" + r.Name, "version": "0.1.0", "description": r.Description,
	}, "", "  ")
	if err := os.WriteFile(filepath.Join(plug, ".claude-plugin", "plugin.json"), append(manifest, '\n'), 0o644); err != nil {
		return res, err
	}
	for s, d := range links {
		if err := os.Symlink(d, filepath.Join(plug, "skills", s)); err != nil {
			return res, err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "system.md"), sys, 0o644); err != nil {
		return res, err
	}
	res.Bytes, res.Skills = len(sys), len(links)
	res.Over = b.Config.BudgetBytes > 0 && res.Bytes > b.Config.BudgetBytes
	return res, nil
}

// RunOpts are the options of `role run`.
type RunOpts struct {
	RC    bool
	Extra []string
}

// Argv returns the command line that starts the tool in a role, built under state.
func (b *Brain) Argv(r *Role, state string, o RunOpts) []string {
	dir := filepath.Join(state, r.Name)
	a := []string{b.Config.Tool, "--setting-sources", "project,local"}
	if p := filepath.Join(r.Dir, "settings.json"); exists(p) {
		a = append(a, "--settings", p)
	}
	a = append(a, "--plugin-dir", filepath.Join(dir, "plugin"),
		"--append-system-prompt-file", filepath.Join(dir, "system.md"))
	if !r.Connectors {
		a = append(a, "--strict-mcp-config")
		if p := filepath.Join(r.Dir, "mcp.json"); exists(p) {
			a = append(a, "--mcp-config", p)
		}
	}
	if r.Model != "" {
		a = append(a, "--model", r.Model)
	}
	if r.PermissionMode != "" {
		a = append(a, "--permission-mode", r.PermissionMode)
	}
	if o.RC {
		n := r.RemoteControlName
		if n == "" {
			n = "role-" + r.Name
		}
		a = append(a, "--remote-control", n)
	}
	return append(a, o.Extra...)
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }
