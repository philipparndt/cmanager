package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// logf appends a timestamped line to ~/.claude/cmanager/cmanager.log. cmanager
// runs mostly invisibly (as a hook, or in a transient popup), so a durable log
// is the way to see what happened. Best-effort: logging never fails the caller.
func logf(format string, args ...any) {
	dir := baseDir()
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "cmanager.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	ts := time.Now().Format("2006-01-02 15:04:05.000")
	fmt.Fprintf(f, ts+" "+format+"\n", args...)
}
