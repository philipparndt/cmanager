// Package agentfs is the on-disk protocol shared by cld (the PTY wrapper)
// and cmanager (the dashboard). Each cld run owns a directory under
// ~/.claude/cmanager/agents/<id>/ containing:
//
//	meta.json   — identity: ids, pids, cwd, terminal size, start time
//	screen.txt  — latest rendered screen (written by cld on a ticker)
//	input.fifo  — a named pipe; cmanager writes bytes, cld feeds them to the PTY
package agentfs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

// resizeTTL is how long a size request from cmanager stays in effect without
// being refreshed. cmanager re-stamps it on every poll while a managed session
// is focused, so if cmanager detaches or dies the size reverts on its own.
const resizeTTL = 4 * time.Second

// Meta identifies one cld-managed session.
type Meta struct {
	ID        string   `json:"id"`
	AgentPID  int      `json:"agentPid"` // the cld process
	ChildPID  int      `json:"childPid"` // the claude process it wraps
	Cwd       string   `json:"cwd"`
	Args      []string `json:"args"`
	Cols      int      `json:"cols"`
	Rows      int      `json:"rows"`
	StartedAt int64    `json:"startedAt"` // epoch ms
}

// Base is ~/.claude/cmanager.
func Base() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "cmanager")
}

func AgentsDir() string           { return filepath.Join(Base(), "agents") }
func AgentDir(id string) string   { return filepath.Join(AgentsDir(), id) }
func MetaPath(id string) string   { return filepath.Join(AgentDir(id), "meta.json") }
func ScreenPath(id string) string { return filepath.Join(AgentDir(id), "screen.txt") }
func InputPath(id string) string  { return filepath.Join(AgentDir(id), "input.fifo") }
func ResizePath(id string) string { return filepath.Join(AgentDir(id), "resize") }

// EnsureDir creates the per-agent directory.
func EnsureDir(id string) error {
	return os.MkdirAll(AgentDir(id), 0o755)
}

// WriteMeta persists identity for an agent.
func WriteMeta(m Meta) error {
	if err := EnsureDir(m.ID); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(MetaPath(m.ID), b, 0o644)
}

// ReadMeta loads one agent's meta by id.
func ReadMeta(id string) (Meta, error) {
	var m Meta
	b, err := os.ReadFile(MetaPath(id))
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(b, &m)
	return m, err
}

// List returns all agents whose cld process is still alive, newest last.
// Stale directories (process gone) are skipped and cleaned up opportunistically.
func List() []Meta {
	entries, err := os.ReadDir(AgentsDir())
	if err != nil {
		return nil
	}
	var metas []Meta
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := ReadMeta(e.Name())
		if err != nil {
			continue
		}
		if !Alive(m.AgentPID) {
			os.RemoveAll(AgentDir(m.ID)) // tidy up after a crashed/exited cld
			continue
		}
		metas = append(metas, m)
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].StartedAt < metas[j].StartedAt })
	return metas
}

// WriteScreen atomically replaces the rendered screen snapshot.
func WriteScreen(id, content string) error {
	tmp := ScreenPath(id) + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, ScreenPath(id))
}

// ReadScreen returns the latest screen snapshot.
func ReadScreen(id string) (string, error) {
	b, err := os.ReadFile(ScreenPath(id))
	return string(b), err
}

// WriteResize records the terminal size cmanager wants the wrapped claude to
// use while it is being viewed. cld honors it over its own terminal size.
func WriteResize(id string, cols, rows int) error {
	if err := EnsureDir(id); err != nil {
		return err
	}
	tmp := ResizePath(id) + ".tmp"
	if err := os.WriteFile(tmp, []byte(fmt.Sprintf("%d %d\n", cols, rows)), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, ResizePath(id))
}

// ReadResize returns the size cmanager requested, or ok=false when there is no
// fresh request (none written, or the last one has gone stale per resizeTTL).
func ReadResize(id string) (cols, rows int, ok bool) {
	p := ResizePath(id)
	fi, err := os.Stat(p)
	if err != nil || time.Since(fi.ModTime()) > resizeTTL {
		return 0, 0, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(string(b), "%d %d", &cols, &rows); err != nil || cols <= 0 || rows <= 0 {
		return 0, 0, false
	}
	return cols, rows, true
}

// ClearResize drops any pending size request (cmanager calls this on detach so
// the wrapped claude snaps back to its own terminal immediately).
func ClearResize(id string) { os.Remove(ResizePath(id)) }

// EnsureFifo creates the input FIFO if absent.
func EnsureFifo(id string) error {
	if err := EnsureDir(id); err != nil {
		return err
	}
	p := InputPath(id)
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	return syscall.Mkfifo(p, 0o600)
}

// SendInput writes bytes into an agent's input FIFO (used by cmanager).
// Non-blocking open fails cleanly if no cld is reading.
func SendInput(id string, data []byte) error {
	f, err := os.OpenFile(InputPath(id), os.O_WRONLY|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(data)
	return err
}

// Remove deletes an agent's directory (called by cld on exit).
func Remove(id string) { os.RemoveAll(AgentDir(id)) }

// Alive reports whether a pid refers to a live process.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
