package role

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
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
	Dir    string // the build: <out>/<role>/builds/<hash>, holding system.md and plugin/
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

// Build writes the role into <out>/<role>/builds/<hash>/ (system.md and plugin/), named by a hash of
// its content, and points <out>/<role>/current at it. A build is never changed once written, so sessions
// started at the same time, or a long-running session while another starts, never see a half-built role.
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
	names := make([]string, 0, len(r.Skills))
	for _, s := range r.Skills {
		d, err := b.ResolveSkill(r, s)
		if err != nil {
			return res, err
		}
		if _, dup := links[s]; !dup {
			names = append(names, s)
		}
		links[s] = d
	}
	sort.Strings(names)
	manifest, _ := json.MarshalIndent(map[string]string{
		"name": "role-" + r.Name, "version": "0.1.0", "description": r.Description,
	}, "", "  ")
	manifest = append(manifest, '\n')

	h := sha256.New()
	h.Write(sys)
	h.Write(manifest)
	for _, s := range names {
		fmt.Fprintf(h, "\x00%s=%s", s, links[s])
	}
	id := hex.EncodeToString(h.Sum(nil))[:16]
	base := filepath.Join(out, r.Name, "builds")
	dir := filepath.Join(base, id)
	res.Dir = dir
	res.Bytes, res.Skills = len(sys), len(links)
	res.Over = b.Config.BudgetBytes > 0 && res.Bytes > b.Config.BudgetBytes

	if !exists(filepath.Join(dir, "system.md")) {
		if err := os.MkdirAll(base, 0o755); err != nil {
			return res, err
		}
		tmp, err := os.MkdirTemp(base, ".tmp-")
		if err != nil {
			return res, err
		}
		if err := writeBuild(tmp, sys, manifest, names, links); err != nil {
			os.RemoveAll(tmp)
			return res, err
		}
		if err := os.Rename(tmp, dir); err != nil {
			os.RemoveAll(tmp)
			if !exists(filepath.Join(dir, "system.md")) { // lost a race only if the winner finished
				return res, err
			}
		}
	}
	// Repoint current atomically: make a uniquely named link, rename it over the old one.
	cur := filepath.Join(out, r.Name, "current")
	if tl, err := os.CreateTemp(filepath.Dir(cur), ".current-"); err == nil {
		name := tl.Name()
		tl.Close()
		os.Remove(name)
		if os.Symlink(filepath.Join("builds", id), name) == nil && os.Rename(name, cur) != nil {
			os.Remove(name)
		}
	}
	pruneBuilds(base, id, 30*24*time.Hour)
	return res, nil
}

func writeBuild(dir string, sys, manifest []byte, names []string, links map[string]string) error {
	plug := filepath.Join(dir, "plugin")
	if err := os.MkdirAll(filepath.Join(plug, ".claude-plugin"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(plug, "skills"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(plug, ".claude-plugin", "plugin.json"), manifest, 0o644); err != nil {
		return err
	}
	for _, s := range names {
		if err := os.Symlink(links[s], filepath.Join(plug, "skills", s)); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(dir, "system.md"), sys, 0o644)
}

// pruneBuilds removes builds older than age, except keep. Old builds may still back a long-running session,
// so the age is generous; failures are ignored.
func pruneBuilds(base, keep string, age time.Duration) {
	ents, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, e := range ents {
		if e.Name() == keep {
			continue
		}
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > age {
			os.RemoveAll(filepath.Join(base, e.Name()))
		}
	}
}

// RunOpts are the options of `role run`.
type RunOpts struct {
	RC    bool
	Extra []string
}

// Argv returns the command line that starts the tool in a role from a build dir (Result.Dir).
func (b *Brain) Argv(r *Role, dir string, o RunOpts) []string {
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
