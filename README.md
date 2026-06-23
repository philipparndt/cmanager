# cmanager

A tmux-native helper for working with many Claude Code sessions at once. It does
two things and lets tmux do everything else:

1. **Notifies you in tmux** when a Claude session in another pane needs your
   input or finishes a turn.
2. **A popup picker** listing every live Claude session with its status — pick
   one and it jumps to that session. Sessions are grouped by how they're reached:
   **tmux** panes first, then a **ghostty** section (macOS surfaces, for sessions
   run outside tmux), then a dimmed **can't jump** section for sessions reachable
   by neither.

It is *not* a terminal multiplexer: there is no screen mirroring, no PTY
wrapping, no pane resizing. tmux already does all of that. cmanager only adds the
thing tmux can't know on its own — which panes are Claude sessions and what
state they're in.

## How it works

- **Session list + status** come from `claude agents --json --all` (busy / idle)
  and the subagent logs under `~/.claude/projects/`.
- **Pane mapping + notifications** come from a Claude Code hook. `cmanager hook`
  runs on session events; it reads `$TMUX_PANE` from its environment to learn
  which pane the session lives in, records it under
  `~/.claude/cmanager/sessions/`, and drives tmux:
  - **Notification** (needs permission / waiting on input) → marks the pane's
    window (`@ai_status = needs`) and flashes a status-line message — unless that
    pane is the one you're already looking at.
  - **Stop** (turn finished) → clears the marker and flashes a "finished"
    message. Intermediate stops (`stop_hook_active`) are ignored.
  - **SessionStart / SessionEnd** → record / drop the pane mapping.

Everything degrades gracefully outside tmux (the hook just no-ops the tmux
calls).

## Install

```sh
make install          # builds and installs bin/cmanager to ~/.local/bin
cmanager setup        # wires the hooks + tmux keybinding (shows a preview, asks first)
```

`cmanager setup` edits `~/.claude/settings.json` and `~/.tmux.conf` for you — it
backs each up first (`.bak-<timestamp>`), shows exactly what it will add, and
only writes after you confirm. It uses this binary's absolute path, so the tmux
popup works even though popups don't load your shell profile. It's idempotent —
re-run it any time. Then reload tmux (`tmux source-file ~/.tmux.conf`) and
restart your Claude sessions so the hooks attach.

The manual steps below are equivalent, if you'd rather wire it yourself.

### 1. Wire the Claude Code hook

Add to `~/.claude/settings.json` (use the full path to `cmanager` if it isn't on
the `PATH` Claude sees):

```json
{
  "hooks": {
    "Notification": [{ "matcher": "", "hooks": [{ "type": "command", "command": "cmanager hook" }] }],
    "Stop":         [{ "matcher": "", "hooks": [{ "type": "command", "command": "cmanager hook" }] }],
    "SessionStart": [{ "matcher": "", "hooks": [{ "type": "command", "command": "cmanager hook" }] }],
    "SessionEnd":   [{ "matcher": "", "hooks": [{ "type": "command", "command": "cmanager hook" }] }]
  }
}
```

### 2. Add the tmux snippet

In `~/.tmux.conf`:

```tmux
# prefix + a → open the session picker in a popup.
# Use an absolute path: tmux popups run via `sh -c` and do NOT source your
# shell profile, so a bare `cmanager` won't be found if ~/.local/bin isn't on
# the tmux server's PATH.
bind a display-popup -E -w 80% -h 70% '$HOME/.local/bin/cmanager pick'

# show ⚠ on windows whose Claude session needs input (set by `cmanager hook`)
set -g window-status-format         '#I:#W#{?#{==:#{@ai_status},needs}, ⚠,}'
set -g window-status-current-format '#I:#W#{?#{==:#{@ai_status},needs}, ⚠,}'
```

Reload with `tmux source-file ~/.tmux.conf`. Requires tmux ≥ 3.2 for
`display-popup`.

> If your shell profile prints anything unconditionally, gate it with
> `[[ $- == *i* ]]` so it doesn't interfere with hook I/O.

## Use

- Run Claude normally inside tmux panes — no wrapper needed.
- When a session in another pane needs you or finishes, you'll see it in the
  status line (and the window gets a ⚠ until you answer).
- Hit `prefix + a` to open the picker. Keys: `↑/↓` move · `enter` jump to the
  pane · `space`/`←`/`→` collapse/expand a session's subagents · `/` filter ·
  `r` refresh · `q`/`esc` dismiss.
- Sessions show their subagents as a tree; a subtree whose work is **all done**
  is collapsed by default, and one with active work stays expanded.
- The panel under the list shows the selected session's directory and **what
  it's currently working on** (its latest prompt).
- It paints instantly from a cache and auto-refreshes (`⟳`) in the background
  while open.

## Commands

| Command          | Role                                                      |
|------------------|-----------------------------------------------------------|
| `cmanager`       | open the picker (alias: `cmanager pick`)                  |
| `cmanager hook`  | Claude Code hook target; reads the event JSON on stdin    |

## Layout

| File            | Purpose                                                       |
|-----------------|---------------------------------------------------------------|
| `main.go`       | subcommand dispatch + shared helpers                          |
| `pick.go`       | the popup picker (bubbletea) + jump (tmux / Ghostty)          |
| `hook.go`       | `cmanager hook`: event → registry + tmux notifications        |
| `tmux.go`       | tmux command helpers (notify, attention, jump, pid→pane)      |
| `ghostty.go`    | Ghostty AppleScript helpers (match a surface by cwd, focus it)|
| `registry.go`   | per-session pane/needs records under `~/.claude/cmanager`     |
| `session.go`    | `claude agents --json` polling                                |
| `tree.go`       | sessions + subagent discovery, flattened for the picker       |

## Notes

- A session started *before* the hook was installed has no recorded pane;
  cmanager falls back to matching the claude pid to a pane via the process tree,
  so jumping still works in most cases.
- A session running directly in a **Ghostty** window (no tmux) is jumpable via
  Ghostty's AppleScript dictionary (≥1.3.0): cmanager matches the surface whose
  working directory equals the session's cwd — preferring the one whose title
  carries Claude's `✳` mark — and `focus`es it. The first such jump may trigger a
  one-time macOS "control Ghostty" automation prompt. Ghostty exposes no pid/tty,
  so if two Claude sessions share a cwd the match can be ambiguous.
- Each running session consumes your subscription quota independently.
