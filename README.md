# cmanager + cld

A terminal dashboard for every Claude Code instance on your machine — shown as a
**tree** (sessions with their subagents nested underneath), with a live detail
view and, for sessions you launch via `cld`, **real interaction**: watch the
live screen in full color and drive the running session remotely — keystrokes,
mode switches, and scrolling all forwarded.

Two binaries:

| Binary     | Role                                                                 |
|------------|----------------------------------------------------------------------|
| `cmanager` | the dashboard TUI                                                    |
| `cld`   | a drop-in wrapper for `claude` that mirrors its screen and accepts remote input |

## Overview (the tree)

```
~/dev/k3c                      ● working      12m
├─ claude-code-guide: fleet…   ✓ done          2m
└─ Explore: find auth paths    ● running       8s
~/dev/vehub/vehub-test ⚡       ○ idle         34m      ← ⚡ = launched via cld
~                              ● needs help: Approve edit to settings.json?
```

- Sessions come from `claude agents --json`; subagents are discovered from
  `~/.claude/projects/<slug>/<sessionId>/subagents/` and nested in the tree.
- Status: 🔴 needs help · 🟡 working · ○ idle (sessions); ● running · ✓ done
  (subagents, inferred from log mtime).
- `⚡` marks a session wrapped by `cld` — those are interactive.

Keys: `↑/↓` move · `enter`/`→` open · `x` stop the selected session · `r`
refresh · `g`/`G` top/bottom · `q` quit.

## Detail view

`enter` on a row opens it:

- **Plain session / subagent** → a **live, read-only** transcript: messages,
  tool calls, results, with markdown + syntax-highlighted code blocks and word
  wrap. Auto-refreshes. `↑/↓` scroll, `esc`/`q` back.
- **cld-managed session (⚡)** → the **live, fully interactive** screen of the
  real Claude UI, **in color**. Every keystroke (and the scroll wheel) is
  forwarded into the running session, so you can type prompts, cycle modes with
  `shift+tab`, navigate menus, and answer permission prompts exactly as if you
  were at its terminal. Press **`Esc` twice** to detach back to cmanager (a
  single `Esc` passes through; `ctrl+c` is forwarded too, so you can interrupt
  the session). While attached, the session is resized to fit the cmanager
  pane, and reverts when you detach. To stop a session entirely, detach and
  press **`x`** on its row in the overview.

## How interaction works (cld)

Run Claude through `cld` instead of `claude`:

```sh
cld                 # instead of: claude
cld --model opus    # any claude args pass straight through
```

You use Claude exactly as normal. Under the hood `cld`:

1. starts `claude` in a **PTY** and tees its output to your terminal,
2. feeds that output through a **VT emulator** to reconstruct the fullscreen UI,
   then re-emits it **with its colors and text attributes** into
   `~/.claude/cmanager/agents/<id>/screen.txt`,
3. records its identity (incl. the claude child PID) in `meta.json`, so
   `cmanager` matches it to the right session row,
4. reads an `input.fifo` and writes anything it receives into the PTY — that's
   how `cmanager`'s forwarded keystrokes and scroll reach the live session,
5. watches a `resize` file and sizes the PTY to whatever cmanager asks for while
   it's focused (falling back to its own terminal otherwise).

## "Needs help" detection

The `claude agents --json` API only reports busy/idle, so cmanager also reads a
`Notification` hook. The hook appends to `~/.claude/cmanager/needs-help.jsonl`
when a session needs permission or input; cmanager flags that row red and clears
it when the session goes busy again (you answered it).

Install the hook once:

```sh
make install-hook
```

then add to `~/.claude/settings.json`:

```json
{ "hooks": { "Notification": [ { "hooks": [
  { "type": "command", "command": "bash /Users/<you>/.claude/cmanager/cmanager-hook.sh" }
] } ] } }
```

## Build / install

```sh
make build      # -> bin/cmanager, bin/cld
make install    # -> ~/.local/bin (override with PREFIX=)
make run        # run the dashboard
make test       # smoke tests
```

## Layout

| File                          | Purpose                                            |
|-------------------------------|----------------------------------------------------|
| `main.go`                     | dashboard TUI: tree, detail, key/scroll forwarding |
| `keys.go`                     | encodes key & scroll events into terminal bytes    |
| `tree.go`                     | sessions + subagent discovery + managed matching   |
| `session.go`                  | `claude agents --json` polling                     |
| `transcript.go`               | transcript rendering (markdown, code, wrap)        |
| `needshelp.go`                | tails the Notification hook's records              |
| `cmd/cld/main.go`             | the PTY wrapper                                    |
| `cmd/cld/screen.go`           | renders the VT grid to colored ANSI                |
| `internal/agentfs/agentfs.go` | on-disk protocol shared by both binaries           |
| `hook/cmanager-hook.sh`       | the `Notification` hook                            |

## Caveats

- While you're attached, the session is resized to the cmanager pane, so its
  **own** terminal (where `cld` runs) will look missized until you detach.
- Keystrokes are reconstructed from cmanager's events, so exotic combos
  (some shift/alt sequences) may not map perfectly; the common navigation,
  mode, and answer keys do.
- Scroll forwarding emits SGR mouse reports, which only do something when the
  session has mouse mode enabled.
- You and `cmanager` share the same input stream into the session.
- Plain (non-`cld`) sessions remain read-only — there is no input channel to
  a `claude` you started directly.
- Each running session consumes your subscription quota independently.
