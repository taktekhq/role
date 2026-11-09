package role

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
)

// Case is one question of an eval suite (one JSON object per line).
//
// A run passes when the answer matches at least one Expect pattern (all of
// them when ExpectAll is set) and none of the Reject patterns. Patterns are
// Go regular expressions, matched case-insensitively.
type Case struct {
	ID        string   `json:"id"`
	Q         string   `json:"q"`
	Expect    []string `json:"expect"`
	ExpectAll bool     `json:"expect_all,omitempty"`
	Reject    []string `json:"reject,omitempty"`
	Note      string   `json:"note,omitempty"`
}

// CaseResult is how one case did over all its runs.
type CaseResult struct {
	ID     string   `json:"id"`
	Passes int      `json:"passes"`
	Runs   int      `json:"runs"`
	Fails  []string `json:"fails,omitempty"` // why each failed run failed
	Answer string   `json:"answer,omitempty"` // the first failing answer, cut to 600 bytes
}

// DefaultSuffix keeps answers short and the eval read-only.
const DefaultSuffix = "\n\n(Eval question: answer in at most three sentences. Look things up if you need to, but do not change, send or deploy anything.)"

// Asker answers one question in a role; tests replace it.
type Asker func(ctx context.Context, q string) (string, error)

// LoadSuite reads a JSONL suite, skipping blank lines and lines starting with #.
func LoadSuite(path string) ([]Case, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Case
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var c Case
		dec := json.NewDecoder(strings.NewReader(line))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		if c.ID == "" || c.Q == "" || len(c.Expect) == 0 {
			return nil, fmt.Errorf("%s:%d: id, q and expect are required", path, n)
		}
		if seen[c.ID] {
			return nil, fmt.Errorf("%s:%d: duplicate id %q", path, n, c.ID)
		}
		seen[c.ID] = true
		for _, p := range append(append([]string{}, c.Expect...), c.Reject...) {
			if _, err := regexp.Compile("(?i)" + p); err != nil {
				return nil, fmt.Errorf("%s:%d: bad pattern %q: %w", path, n, p, err)
			}
		}
		out = append(out, c)
	}
	return out, sc.Err()
}

// Grade says whether an answer passes a case, and why not.
func Grade(c Case, answer string) (bool, string) {
	match := func(p string) bool { return regexp.MustCompile("(?i)" + p).MatchString(answer) }
	hit := 0
	for _, p := range c.Expect {
		if match(p) {
			hit++
		}
	}
	if hit == 0 || (c.ExpectAll && hit < len(c.Expect)) {
		return false, fmt.Sprintf("expected %v", c.Expect)
	}
	for _, p := range c.Reject {
		if match(p) {
			return false, fmt.Sprintf("rejected %q", p)
		}
	}
	return true, ""
}

// Eval runs every case runs times with up to jobs at once.
func Eval(ctx context.Context, cases []Case, runs, jobs int, timeout time.Duration, ask Asker) []CaseResult {
	type job struct{ ci, run int }
	res := make([]CaseResult, len(cases))
	for i, c := range cases {
		res[i] = CaseResult{ID: c.ID, Runs: runs}
	}
	var mu sync.Mutex
	ch := make(chan job)
	var wg sync.WaitGroup
	for w := 0; w < max(1, jobs); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range ch {
				c := cases[j.ci]
				cctx, cancel := context.WithTimeout(ctx, timeout)
				ans, err := ask(cctx, c.Q)
				cancel()
				ok, why := false, ""
				if err != nil {
					why = "error: " + err.Error()
				} else {
					ok, why = Grade(c, ans)
				}
				mu.Lock()
				if ok {
					res[j.ci].Passes++
				} else {
					res[j.ci].Fails = append(res[j.ci].Fails, why)
					if res[j.ci].Answer == "" {
						res[j.ci].Answer = strings.TrimSpace(ans)
						if len(res[j.ci].Answer) > 600 {
							res[j.ci].Answer = res[j.ci].Answer[:600]
						}
					}
				}
				mu.Unlock()
			}
		}()
	}
	for r := 0; r < runs; r++ {
		for i := range cases {
			ch <- job{i, r}
		}
	}
	close(ch)
	wg.Wait()
	return res
}

// Summary is pass@1 (share of all runs that passed) and pass^k (share of
// cases that passed every run).
func Summary(res []CaseResult) (runsPassed, runs, casesAll, cases int) {
	for _, r := range res {
		runsPassed += r.Passes
		runs += r.Runs
		if r.Passes == r.Runs {
			casesAll++
		}
	}
	return runsPassed, runs, casesAll, len(res)
}

func cmdEval(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	bf := fs.String("brain", "", "brain dir")
	suite := fs.String("suite", "", "JSONL suite (default <brain>/tests/recall.jsonl)")
	runs := fs.Int("runs", 1, "runs per case (pass^k uses all of them)")
	jobs := fs.Int("jobs", 3, "cases at once")
	timeout := fs.Duration("timeout", 4*time.Minute, "per run")
	only := fs.String("only", "", "comma-separated case ids")
	jsonOut := fs.String("json", "", "also write results to this file")
	suffix := fs.String("suffix", DefaultSuffix, "appended to every question")
	b, pos, err := open(fs, bf, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: role eval <name> [--suite FILE] [--runs N] [--jobs N] [--timeout D] [--only ids] [--json FILE]")
	}
	r, err := b.Role(pos[0])
	if err != nil {
		return err
	}
	if *suite == "" {
		*suite = filepath.Join(b.Dir, "tests", "recall.jsonl")
	}
	cases, err := LoadSuite(*suite)
	if err != nil {
		return err
	}
	if *only != "" {
		want := map[string]bool{}
		for _, id := range strings.Split(*only, ",") {
			want[strings.TrimSpace(id)] = true
		}
		var keep []Case
		for _, c := range cases {
			if want[c.ID] {
				keep = append(keep, c)
			}
		}
		cases = keep
	}
	res0, err := b.Build(r, StateRoot())
	if err != nil {
		return err
	}
	ask := func(ctx context.Context, q string) (string, error) {
		argv := b.Argv(r, res0.Dir, RunOpts{Extra: []string{"-p", q + *suffix}})
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir = filepath.Dir(b.Dir)
		var so, se bytes.Buffer
		cmd.Stdout, cmd.Stderr = &so, &se
		if err := cmd.Run(); err != nil {
			msg := strings.TrimSpace(se.String())
			if len(msg) > 200 {
				msg = msg[:200]
			}
			return so.String(), fmt.Errorf("%v %s", err, msg)
		}
		return so.String(), nil
	}
	start := time.Now()
	res := Eval(context.Background(), cases, *runs, *jobs, *timeout, ask)
	sort.SliceStable(res, func(i, j int) bool { return res[i].Passes*res[j].Runs < res[j].Passes*res[i].Runs })
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "CASE\tPASS\tWHY (first failure)")
	for _, x := range res {
		why := ""
		if len(x.Fails) > 0 {
			why = x.Fails[0]
			if len(why) > 120 {
				why = why[:120]
			}
		}
		fmt.Fprintf(tw, "%s\t%d/%d\t%s\n", x.ID, x.Passes, x.Runs, why)
	}
	tw.Flush()
	rp, rn, ca, cn := Summary(res)
	fmt.Fprintf(out, "role %s: pass@1 %d/%d runs (%.0f%%), pass^%d %d/%d cases, %s\n",
		r.Name, rp, rn, pct(rp, rn), *runs, ca, cn, time.Since(start).Round(time.Second))
	if *jsonOut != "" {
		bs, _ := json.MarshalIndent(map[string]any{"role": r.Name, "suite": *suite, "runs": *runs,
			"at": time.Now().UTC().Format(time.RFC3339), "results": res}, "", "  ")
		if err := os.WriteFile(*jsonOut, append(bs, '\n'), 0o644); err != nil {
			return err
		}
	}
	if rp < rn {
		return fmt.Errorf("%d of %d runs failed", rn-rp, rn)
	}
	return nil
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}
