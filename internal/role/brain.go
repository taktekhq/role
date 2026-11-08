// Package role assembles Claude Code sessions from a "brain" directory:
// a few start files, one prompt, a few skills, settings and MCP servers per role.
package role

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Config is roles/roles.json, shared by every role.
type Config struct {
	Start       []string `json:"start"`
	SkillPaths  []string `json:"skill_paths"`
	BudgetBytes int      `json:"budget_bytes"`
	Tool        string   `json:"tool"`
}

// Role is roles/<name>/role.json plus its name and directory.
type Role struct {
	Name              string   `json:"-"`
	Dir               string   `json:"-"`
	Description       string   `json:"description"`
	Skills            []string `json:"skills"`
	Connectors        bool     `json:"connectors"`
	Model             string   `json:"model"`
	PermissionMode    string   `json:"permission_mode"`
	RemoteControlName string   `json:"remote_control_name"`
}

// Brain is a loaded brain directory.
type Brain struct {
	Dir    string
	Config Config
}

// FindBrain returns the brain dir from the flag, ROLE_BRAIN, or the default places.
func FindBrain(flagVal string) (string, error) {
	home, _ := os.UserHomeDir()
	cands := []string{flagVal, os.Getenv("ROLE_BRAIN"),
		filepath.Join(home, "taktekhq/brain"), filepath.Join(home, "work/taktekhq/brain")}
	for i, c := range cands {
		if c == "" {
			continue
		}
		c = expandHome(c)
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return filepath.Abs(c)
		} else if i < 2 {
			return "", fmt.Errorf("brain dir %q (from %s) is not a directory", c, []string{"--brain", "ROLE_BRAIN"}[i])
		}
	}
	return "", errors.New("no brain found: pass --brain DIR, set ROLE_BRAIN, or create ~/taktekhq/brain")
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func validJSONFile(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !json.Valid(b) {
		return fmt.Errorf("%s: invalid JSON", path)
	}
	return nil
}

// Load reads roles/roles.json from the brain.
func Load(dir string) (*Brain, error) {
	b := &Brain{Dir: dir, Config: Config{BudgetBytes: 9000, Tool: "claude"}}
	if err := readJSON(filepath.Join(dir, "roles", "roles.json"), &b.Config); err != nil {
		return nil, err
	}
	if t := os.Getenv("ROLE_TOOL"); t != "" {
		b.Config.Tool = t
	}
	if b.Config.Tool == "" {
		b.Config.Tool = "claude"
	}
	return b, nil
}

// Roles lists every role (a directory under roles/ holding role.json), sorted by name.
func (b *Brain) Roles() ([]Role, error) {
	ents, err := os.ReadDir(filepath.Join(b.Dir, "roles"))
	if err != nil {
		return nil, err
	}
	var out []Role
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		r, err := b.Role(e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Role loads one role by name.
func (b *Brain) Role(name string) (*Role, error) {
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return nil, fmt.Errorf("invalid role name %q", name)
	}
	r := &Role{Name: name, Dir: filepath.Join(b.Dir, "roles", name)}
	if err := readJSON(filepath.Join(r.Dir, "role.json"), r); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("unknown role %q (no %s)", name, filepath.Join(r.Dir, "role.json"))
		}
		return nil, fmt.Errorf("role %s: %w", name, err)
	}
	return r, nil
}

// ResolveSkill finds the first <skill_path>/<skill> dir that holds SKILL.md.
func (b *Brain) ResolveSkill(r *Role, skill string) (string, error) {
	if skill == "" || skill != filepath.Base(skill) {
		return "", fmt.Errorf("role %s: invalid skill name %q", r.Name, skill)
	}
	for _, sp := range b.Config.SkillPaths {
		sp = expandHome(sp)
		if !filepath.IsAbs(sp) {
			sp = filepath.Join(b.Dir, sp)
		}
		d := filepath.Join(sp, skill)
		if _, err := os.Stat(filepath.Join(d, "SKILL.md")); err == nil {
			return filepath.Abs(d)
		}
	}
	return "", fmt.Errorf("role %s: skill %q not found in skill_paths %v", r.Name, skill, b.Config.SkillPaths)
}
