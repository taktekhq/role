package role

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadSuite(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "s.jsonl")
	os.WriteFile(p, []byte(`# comment
{"id":"a","q":"can you?","expect":["yes"],"reject":["no way"]}

{"id":"b","q":"where?","expect":["x\\.md","y"],"expect_all":true}
`), 0o644)
	cs, err := LoadSuite(p)
	if err != nil || len(cs) != 2 || !cs[1].ExpectAll {
		t.Fatalf("got %v %v", cs, err)
	}
	for name, body := range map[string]string{
		"dup":     `{"id":"a","q":"q","expect":["x"]}` + "\n" + `{"id":"a","q":"q","expect":["x"]}`,
		"missing": `{"id":"a","q":"q"}`,
		"badre":   `{"id":"a","q":"q","expect":["("]}`,
		"unknown": `{"id":"a","q":"q","expect":["x"],"zzz":1}`,
	} {
		os.WriteFile(p, []byte(body), 0o644)
		if _, err := LoadSuite(p); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestGrade(t *testing.T) {
	c := Case{ID: "a", Q: "q", Expect: []string{"bin/get", "vault"}, Reject: []string{"cannot"}}
	if ok, _ := Grade(c, "Use BIN/GET on the Mac"); !ok {
		t.Error("case-insensitive any-of should pass")
	}
	if ok, why := Grade(c, "I cannot, but bin/get"); ok || !strings.Contains(why, "rejected") {
		t.Errorf("reject should fail: %v %s", ok, why)
	}
	c.ExpectAll = true
	if ok, _ := Grade(c, "bin/get only"); ok {
		t.Error("expect_all needs every pattern")
	}
}

func TestEvalCountsAndErrors(t *testing.T) {
	cases := []Case{
		{ID: "good", Q: "g", Expect: []string{"ok"}},
		{ID: "flaky", Q: "f", Expect: []string{"ok"}},
		{ID: "err", Q: "e", Expect: []string{"ok"}},
	}
	n := 0
	ask := func(ctx context.Context, q string) (string, error) {
		switch q {
		case "g":
			return "ok", nil
		case "f":
			n++
			if n%2 == 0 {
				return "nope", nil
			}
			return "ok", nil
		}
		return "", errors.New("boom")
	}
	res := Eval(context.Background(), cases, 2, 1, time.Second, ask)
	rp, rn, ca, cn := Summary(res)
	if rp != 3 || rn != 6 || ca != 1 || cn != 3 {
		t.Fatalf("summary %d/%d %d/%d: %+v", rp, rn, ca, cn, res)
	}
	if !strings.HasPrefix(res[2].Fails[0], "error: boom") {
		t.Errorf("error reason: %v", res[2].Fails)
	}
}

func TestEvalCommandWithFakeTool(t *testing.T) {
	d := fixture(t, "100000")
	bin := t.TempDir()
	// The fake tool answers with its last argument (the question) plus "ok".
	os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\nfor a; do last=$a; done\necho \"$last ok\"\n"), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	write(t, d, "tests/recall.jsonl", `{"id":"one","q":"ping","expect":["ping ok"]}`+"\n"+`{"id":"two","q":"pong","expect":["never"]}`)
	var out strings.Builder
	err := cmdEval([]string{"--brain", d, "--json", filepath.Join(d, "r.json"), "--suffix", "", "a"}, &out)
	if err == nil || !strings.Contains(err.Error(), "1 of 2 runs failed") {
		t.Fatalf("err %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "pass@1 1/2") {
		t.Errorf("summary missing:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(d, "r.json")); err != nil {
		t.Error("json not written")
	}
}
