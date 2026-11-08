package role

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixture makes a brain with two roles: a (strict, mcp+settings) and b (connectors).
func fixture(t *testing.T, budget string) string {
	t.Helper()
	d := t.TempDir()
	write(t, d, "roles/roles.json", `{"start":["s1.md","s2.md"],"skill_paths":["sk"],"budget_bytes":`+budget+`,"tool":"claude"}`)
	write(t, d, "s1.md", "ONE")
	write(t, d, "s2.md", "TWO\n")
	write(t, d, "sk/x/SKILL.md", "x")
	write(t, d, "sk/y/SKILL.md", "y")
	write(t, d, "roles/a/role.json", `{"description":"role a","skills":["x","y"],"connectors":false,"model":"opus","permission_mode":"plan"}`)
	write(t, d, "roles/a/prompt.md", "PROMPT A")
	write(t, d, "roles/a/settings.json", `{}`)
	write(t, d, "roles/a/mcp.json", `{"mcpServers":{}}`)
	write(t, d, "roles/b/role.json", `{"description":"role b","skills":[],"connectors":true,"remote_control_name":"deskb"}`)
	write(t, d, "roles/b/prompt.md", "PROMPT B")
	return d
}

func load(t *testing.T, d string) *Brain {
	t.Helper()
	b, err := Load(d)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBuild(t *testing.T) {
	d := fixture(t, "9000")
	b := load(t, d)
	r, _ := b.Role("a")
	out := t.TempDir()
	res, err := b.Build(r, out)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skills != 2 || res.Over {
		t.Fatalf("bad result %+v", res)
	}
	sys, _ := os.ReadFile(filepath.Join(out, "a", "system.md"))
	want := "<!-- from: s1.md -->\nONE\n\n<!-- from: s2.md -->\nTWO\n\n<!-- from: roles/a/prompt.md -->\nPROMPT A\n\n"
	if string(sys) != want || res.Bytes != len(want) {
		t.Fatalf("system.md = %q", sys)
	}
	m, _ := os.ReadFile(filepath.Join(out, "a", "plugin", ".claude-plugin", "plugin.json"))
	if !strings.Contains(string(m), `"name": "role-a"`) {
		t.Fatalf("manifest %s", m)
	}
	if _, err := os.Stat(filepath.Join(out, "a", "plugin", "skills", "x", "SKILL.md")); err != nil {
		t.Fatal("skill symlink does not resolve:", err)
	}
	// Rebuild after removing a skill: stale link must go.
	write(t, d, "roles/a/role.json", `{"description":"role a","skills":["x"]}`)
	r, _ = b.Role("a")
	if _, err := b.Build(r, out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(out, "a", "plugin", "skills", "y")); err == nil {
		t.Fatal("stale skill link left behind")
	}
}

func TestSkillResolutionOrder(t *testing.T) {
	d := fixture(t, "9000")
	write(t, d, "sk2/x/SKILL.md", "other")
	write(t, d, "sk/empty/readme", "no SKILL.md")
	write(t, d, "roles/roles.json", `{"start":[],"skill_paths":["sk/empty","sk2","sk"]}`)
	b := load(t, d)
	r, _ := b.Role("a")
	got, err := b.ResolveSkill(r, "x")
	if err != nil || filepath.Base(filepath.Dir(got)) != "sk2" {
		t.Fatalf("got %q, %v", got, err)
	}
	got, err = b.ResolveSkill(r, "y")
	if err != nil || filepath.Base(filepath.Dir(got)) != "sk" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestBudgetWarn(t *testing.T) {
	d := fixture(t, "10")
	b := load(t, d)
	var out, errw bytes.Buffer
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := cmdBuild([]string{"--brain", d, "a"}, &out, &errw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errw.String(), "WARN a") {
		t.Fatalf("no warning: %q", errw.String())
	}
	_ = b
}

func TestCheckFailures(t *testing.T) {
	cases := map[string]struct {
		mutate func(t *testing.T, d string)
		budget string
		want   string
	}{
		"ok":             {func(t *testing.T, d string) {}, "9000", ""},
		"over budget":    {func(t *testing.T, d string) {}, "20", "over budget"},
		"missing skill":  {func(t *testing.T, d string) { write(t, d, "roles/a/role.json", `{"skills":["nope"]}`) }, "9000", `skill "nope"`},
		"missing start":  {func(t *testing.T, d string) { os.Remove(filepath.Join(d, "s1.md")) }, "9000", "s1.md"},
		"missing prompt": {func(t *testing.T, d string) { os.Remove(filepath.Join(d, "roles/b/prompt.md")) }, "9000", "prompt.md"},
		"bad settings":   {func(t *testing.T, d string) { write(t, d, "roles/a/settings.json", `{`) }, "9000", "invalid JSON"},
		"bad mcp":        {func(t *testing.T, d string) { write(t, d, "roles/a/mcp.json", `nope`) }, "9000", "invalid JSON"},
		"unknown key":    {func(t *testing.T, d string) { write(t, d, "roles/a/role.json", `{"skils":[]}`) }, "9000", "unknown field"},
		"bad role json":  {func(t *testing.T, d string) { write(t, d, "roles/a/role.json", `{`) }, "9000", "role a"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			d := fixture(t, c.budget)
			c.mutate(t, d)
			var out bytes.Buffer
			err := cmdCheck([]string{"--brain", d}, &out)
			if c.want == "" {
				if err != nil {
					t.Fatalf("unexpected: %v\n%s", err, out.String())
				}
				return
			}
			if err == nil || !strings.Contains(out.String(), c.want) {
				t.Fatalf("want failure containing %q, err=%v\n%s", c.want, err, out.String())
			}
		})
	}
}

func TestUnknownConfigKey(t *testing.T) {
	d := fixture(t, "9000")
	write(t, d, "roles/roles.json", `{"start":[],"bogus":1}`)
	if _, err := Load(d); err == nil {
		t.Fatal("expected error")
	}
}

func TestArgv(t *testing.T) {
	d := fixture(t, "9000")
	b := load(t, d)
	st := "/state"
	a, _ := b.Role("a")
	got := strings.Join(b.Argv(a, st, RunOpts{Extra: []string{"-p", "hi"}}), " ")
	want := "claude --setting-sources project,local --settings " + d + "/roles/a/settings.json" +
		" --plugin-dir /state/a/plugin --append-system-prompt-file /state/a/system.md" +
		" --strict-mcp-config --mcp-config " + d + "/roles/a/mcp.json --model opus --permission-mode plan -p hi"
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
	// Strict without mcp.json: no --mcp-config.
	os.Remove(filepath.Join(d, "roles/a/mcp.json"))
	os.Remove(filepath.Join(d, "roles/a/settings.json"))
	got = strings.Join(b.Argv(a, st, RunOpts{RC: true}), " ")
	if strings.Contains(got, "--mcp-config") || strings.Contains(got, "--settings") || !strings.Contains(got, "--strict-mcp-config") ||
		!strings.HasSuffix(got, "--remote-control role-a") {
		t.Fatalf("got %s", got)
	}
	// Connectors true: never strict, even with an mcp.json present.
	write(t, d, "roles/b/mcp.json", `{}`)
	br, _ := b.Role("b")
	got = strings.Join(b.Argv(br, st, RunOpts{RC: true}), " ")
	if strings.Contains(got, "--strict-mcp-config") || strings.Contains(got, "--mcp-config") {
		t.Fatalf("connectors role must not be strict: %s", got)
	}
	if !strings.HasSuffix(got, "--remote-control deskb") {
		t.Fatalf("got %s", got)
	}
}

func TestRunDryRunAndExec(t *testing.T) {
	d := fixture(t, "9000")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var out bytes.Buffer
	if err := cmdRun([]string{"--brain", d, "b", "--dry-run", "--rc", "--", "--verbose"}, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if lines[0] != "claude" || lines[len(lines)-1] != "--verbose" || !strings.Contains(out.String(), "deskb") {
		t.Fatalf("argv:\n%s", out.String())
	}
	// ROLE_TOOL override and exec call.
	bin := t.TempDir()
	write(t, bin, "faketool", "#!/bin/sh\n")
	os.Chmod(filepath.Join(bin, "faketool"), 0o755)
	t.Setenv("ROLE_TOOL", filepath.Join(bin, "faketool"))
	var gotArgv []string
	old := execFn
	execFn = func(path string, argv []string, env []string) error { gotArgv = argv; return nil }
	defer func() { execFn = old }()
	if err := cmdRun([]string{"--brain", d, "a"}, &out); err != nil {
		t.Fatal(err)
	}
	if len(gotArgv) == 0 || gotArgv[0] != filepath.Join(bin, "faketool") {
		t.Fatalf("exec argv %v", gotArgv)
	}
}

func TestFindBrain(t *testing.T) {
	d := t.TempDir()
	t.Setenv("ROLE_BRAIN", "")
	t.Setenv("HOME", t.TempDir())
	if _, err := FindBrain(""); err == nil {
		t.Fatal("expected no brain")
	}
	t.Setenv("ROLE_BRAIN", d)
	if got, err := FindBrain(""); err != nil || got != d {
		t.Fatalf("env: %q %v", got, err)
	}
	flagDir := t.TempDir()
	if got, _ := FindBrain(flagDir); got != flagDir {
		t.Fatalf("flag should win, got %q", got)
	}
	if _, err := FindBrain(filepath.Join(d, "missing")); err == nil {
		t.Fatal("bad flag should error")
	}
}

func TestLsAndShow(t *testing.T) {
	d := fixture(t, "9000")
	var out bytes.Buffer
	if err := cmdLs([]string{"--brain", d}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "role a") || !strings.Contains(out.String(), "role b") {
		t.Fatal(out.String())
	}
	out.Reset()
	if err := cmdShow([]string{"--brain", d, "a"}, &out); err != nil || !strings.Contains(out.String(), "PROMPT A") {
		t.Fatalf("%v %s", err, out.String())
	}
}

func TestExampleBrainPasses(t *testing.T) {
	var out bytes.Buffer
	if err := cmdCheck([]string{"--brain", "../../example"}, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
}
