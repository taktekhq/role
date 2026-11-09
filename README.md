# role

Start Claude Code as a specialist, not as a generalist with everything loaded.

One agent setup usually grows into a pile: a long user `CLAUDE.md`, dozens of skills, plugins, memory, every connector. Every session pays for all of it, and a reviewer session can edit files because the writer session could. `role` keeps one shared brain and starts each session in a **role**: a small set of start files, that role's prompt, only that role's skills, that role's permissions and hooks, and that role's MCP servers. Everything at user level is skipped.

`role` is a thin launcher. It builds files and then runs `claude` with the right flags. It has no dependencies.

## Install

```
go install github.com/taktekhq/role@latest
```

## The brain directory

`role` reads a "brain" directory. It finds it from `--brain DIR`, else `$ROLE_BRAIN`, else `$XDG_CONFIG_HOME/role/brain` (default `~/.config/role/brain`; a symlink to wherever you keep it is fine).

```
brain/
  guide/memory.md              start files: any files you want every role to read
  capabilities/INDEX.md
  roles/
    roles.json                 shared config
    CORE.md                    (a start file here, by convention)
    reviewer/
      role.json                the role's config
      prompt.md                the role's prompt
      settings.json            optional, passed to --settings (permissions, hooks)
      mcp.json                 optional, passed to --mcp-config
```

`roles/roles.json`:

```json
{
  "start": ["guide/memory.md", "capabilities/INDEX.md", "roles/CORE.md"],
  "skill_paths": ["~/.claude/skills"],
  "budget_bytes": 9000,
  "tool": "claude"
}
```

- `start`: files relative to the brain dir. They are placed first in the system prompt, in this order.
- `skill_paths`: where skills live. `~` expands. A relative path is relative to the brain dir. A skill name resolves to the first `<path>/<name>` that contains `SKILL.md`.
- `budget_bytes`: the size you allow for the assembled system prompt. `build` warns above it, `check` fails above it. This is how you keep session start small.
- `tool`: the command to run. Override with `$ROLE_TOOL`.

`roles/<name>/role.json`:

```json
{
  "description": "Reviews diffs, read-only",
  "skills": ["checklist"],
  "connectors": false,
  "model": "",
  "permission_mode": "plan",
  "remote_control_name": ""
}
```

Unknown keys are errors, so typos do not pass silently. Empty strings mean "do not pass the flag".

## Example with two roles

The `example/` directory in this repo is a complete brain.

```
$ role ls --brain example
ROLE      SKILLS  DESCRIPTION
reviewer  1       Reviews diffs, read-only
scribe    2       Writes docs and release notes

$ export ROLE_BRAIN=$PWD/example
$ role check
$ role run reviewer --dry-run
```

`reviewer` has `settings.json` that denies `Edit` and `Write`, runs in plan mode, and is strict about MCP (no connectors). `scribe` loads claude.ai connectors and has two skills. Start one:

```
role run reviewer
role run scribe --rc -- --resume
```

## Commands

```
role ls                              roles, descriptions, skill counts
role build [name...]                 write system.md and the skill plugin (default: all roles)
role show <name>                     print the assembled system prompt
role check                           build everything into a temp dir; exit 1 on any problem
role run <name> [--dry-run] [--rc] [-- extra args]
role version
```

All commands except `version` accept `--brain DIR`.

- `build` writes `$XDG_STATE_HOME/role/<name>/builds/<hash>/` (default `~/.local/state/role/...`): `system.md` (each start file, then `prompt.md`, each under a `<!-- from: path -->` marker) and `plugin/`. The folder is named by a hash of its content and never changed after it is written, so many sessions can start at once and a long-running session keeps its files while you edit the role. `<name>/current` links to the newest build; builds older than 30 days are pruned. It prints the size of `system.md` and warns when over budget.
- `check` fails on a missing file, a missing skill, invalid JSON, unknown keys or a prompt over budget, and prints a table.
- `run` builds, then replaces itself with the tool (`syscall.Exec`), so the tool owns the terminal. `--rc` adds `--remote-control <remote_control_name, else role-NAME>`. Anything after `--` is passed through. `--dry-run` prints the command, one argument per line.

## How it maps to Claude Code

`role run reviewer` runs:

```
claude --setting-sources project,local \
       --settings <brain>/roles/reviewer/settings.json \
       --plugin-dir ~/.local/state/role/reviewer/builds/<hash>/plugin \
       --append-system-prompt-file ~/.local/state/role/reviewer/builds/<hash>/system.md \
       --strict-mcp-config [--mcp-config <brain>/roles/reviewer/mcp.json] \
       [--model X] [--permission-mode Y] [--remote-control NAME] [extra args]
```

- `--setting-sources project,local` drops user-level settings, user `CLAUDE.md`, user skills and user plugins. Project and local settings in the current directory still apply.
- The generated plugin holds symlinks to the role's skills. Claude Code shows them namespaced as `role-<name>:<skill>`.
- `connectors: false` adds `--strict-mcp-config`, so only the role's `mcp.json` servers load. `connectors: true` omits it, and claude.ai connectors load.

## Limits

- Claude Code only for now. Codex and Gemini are planned.
- Permissions are only as strong as Claude Code's settings and your sandbox. `role` passes the settings through. It does not enforce anything itself, and a project-level settings file in the working directory can still add rules.
- Verified on Claude Code 2.1.318. Flags may change in later versions.
- Skills are linked from where they live. Editing a skill takes effect on the next session, no rebuild needed. Adding or removing a skill in `role.json` needs `role build` (or `role run`, which builds).

## License

MIT. Copyright 2026 Taktek and Nizar Mahmoud.

## Eval

`role eval <name>` asks a role every question of a JSONL suite (default `<brain>/tests/recall.jsonl`) and grades each answer by pattern:

```json
{"id":"secret-get","q":"How do I get the Cloudflare token?","expect":["bin/get"],"reject":["paste it"]}
```

`expect` is any-of (all-of with `"expect_all": true`), `reject` is none-of; both are Go regexps matched case-insensitively. `--runs 3` repeats each case and reports pass@1 (share of runs) and pass^k (share of cases that passed every run). `--json FILE` keeps the results, with the first failing answer of each case. A suffix asks for short, read-only answers (`--suffix ""` turns it off). It exits non-zero when any run fails, so it can gate a change to the brain.
