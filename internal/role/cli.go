package role

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"text/tabwriter"
)

// Version is set at build time or left as is.
var Version = "0.1.0"

// execFn replaces the process; tests override it.
var execFn = syscall.Exec

const usage = `role starts Claude Code in a role.

Usage:
  role ls                          list roles
  role build [name...]             write system.md and the skill plugin for roles (default: all)
  role show <name>                 print the assembled system prompt
  role check                       build everything in a temp dir, fail on any problem
  role run <name> [--dry-run] [--rc] [-- extra args]
  role eval <name> [--suite FILE] [--runs N] [--jobs N] [--only ids] [--json FILE]
                                   ask the role every question of a JSONL suite, grade by pattern
  role version

Every command except version takes --brain DIR (else $ROLE_BRAIN, else ~/.config/role/brain).
`

// Main runs the CLI and returns the exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "ls":
		err = cmdLs(rest, stdout)
	case "build":
		err = cmdBuild(rest, stdout, stderr)
	case "show":
		err = cmdShow(rest, stdout)
	case "check":
		err = cmdCheck(rest, stdout)
	case "run":
		err = cmdRun(rest, stdout)
	case "eval":
		err = cmdEval(rest, stdout)
	case "version":
		fmt.Fprintln(stdout, "role", Version)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
	default:
		fmt.Fprintf(stderr, "role: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "role:", err)
		return 1
	}
	return 0
}

// parse parses flags that may come before or after positional args.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func open(fs *flag.FlagSet, brainFlag *string, args []string) (*Brain, []string, error) {
	pos, err := parse(fs, args)
	if err != nil {
		return nil, nil, err
	}
	dir, err := FindBrain(*brainFlag)
	if err != nil {
		return nil, nil, err
	}
	b, err := Load(dir)
	return b, pos, err
}

func cmdLs(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	bf := fs.String("brain", "", "brain dir")
	b, _, err := open(fs, bf, args)
	if err != nil {
		return err
	}
	rs, err := b.Roles()
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ROLE\tSKILLS\tDESCRIPTION")
	for _, r := range rs {
		fmt.Fprintf(tw, "%s\t%d\t%s\n", r.Name, len(r.Skills), r.Description)
	}
	return tw.Flush()
}

func cmdBuild(args []string, out, errw io.Writer) error {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	bf := fs.String("brain", "", "brain dir")
	b, names, err := open(fs, bf, args)
	if err != nil {
		return err
	}
	roles, err := selectRoles(b, names)
	if err != nil {
		return err
	}
	for _, r := range roles {
		res, err := b.Build(&r, StateRoot())
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: system.md %d bytes, %d skills\n", r.Name, res.Bytes, res.Skills)
		if res.Over {
			fmt.Fprintf(errw, "WARN %s: %d bytes is over the budget of %d\n", r.Name, res.Bytes, res.Budget)
		}
	}
	return nil
}

func selectRoles(b *Brain, names []string) ([]Role, error) {
	if len(names) == 0 {
		return b.Roles()
	}
	var out []Role
	for _, n := range names {
		r, err := b.Role(n)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, nil
}

func cmdShow(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	bf := fs.String("brain", "", "brain dir")
	b, pos, err := open(fs, bf, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: role show <name>")
	}
	r, err := b.Role(pos[0])
	if err != nil {
		return err
	}
	sys, err := b.System(r)
	if err != nil {
		return err
	}
	_, err = out.Write(sys)
	return err
}

func cmdCheck(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	bf := fs.String("brain", "", "brain dir")
	b, _, err := open(fs, bf, args)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "role-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	// Load each role on its own so one broken role does not hide the rest.
	ents, err := os.ReadDir(b.Dir + "/roles")
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ROLE\tBYTES\tBUDGET\tSKILLS\tSTATUS")
	failed := 0
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		r, err := b.Role(e.Name())
		if err != nil {
			fmt.Fprintf(tw, "%s\t-\t-\t-\tFAIL %v\n", e.Name(), err)
			failed++
			continue
		}
		res, err := b.Build(r, tmp)
		switch {
		case err != nil:
			fmt.Fprintf(tw, "%s\t-\t%d\t-\tFAIL %v\n", r.Name, res.Budget, err)
			failed++
		case res.Over:
			fmt.Fprintf(tw, "%s\t%d\t%d\t%d\tFAIL over budget\n", r.Name, res.Bytes, res.Budget, res.Skills)
			failed++
		default:
			fmt.Fprintf(tw, "%s\t%d\t%d\t%d\tok\n", r.Name, res.Bytes, res.Budget, res.Skills)
		}
	}
	tw.Flush()
	if failed > 0 {
		return fmt.Errorf("%d role(s) failed", failed)
	}
	return nil
}

func cmdRun(args []string, out io.Writer) error {
	var extra []string
	for i, a := range args {
		if a == "--" {
			args, extra = args[:i], args[i+1:]
			break
		}
	}
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	bf := fs.String("brain", "", "brain dir")
	dry := fs.Bool("dry-run", false, "print the command and exit")
	rc := fs.Bool("rc", false, "add --remote-control")
	b, pos, err := open(fs, bf, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: role run <name> [--dry-run] [--rc] [-- extra args]")
	}
	r, err := b.Role(pos[0])
	if err != nil {
		return err
	}
	res, err := b.Build(r, StateRoot())
	if err != nil {
		return err
	}
	argv := b.Argv(r, res.Dir, RunOpts{RC: *rc, Extra: extra})
	if *dry {
		fmt.Fprintln(out, strings.Join(argv, "\n"))
		return nil
	}
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	return execFn(path, argv, os.Environ())
}
