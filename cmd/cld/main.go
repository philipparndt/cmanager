// cld wraps `claude` in a PTY. You use Claude exactly as normal in your
// terminal; meanwhile cld mirrors the screen and exposes an input channel so
// cmanager can watch the live session and inject prompts into it.
//
// Usage: cld [claude args...]   (a drop-in replacement for `claude`)
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"cmanager/internal/agentfs"

	"github.com/creack/pty"
	"github.com/hinshun/vt10x"
	"golang.org/x/term"
)

// frameInterval caps how often we re-render the mirror in response to a burst
// of PTY output — ~60fps, so typing feels live without thrashing on every
// chunk. reconcileInterval is a slower heartbeat that keeps the PTY sized
// correctly (and resizes claude back to its own terminal after cmanager
// detaches) even when the session is idle and producing no output.
const (
	frameInterval     = 16 * time.Millisecond
	reconcileInterval = 50 * time.Millisecond
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "cld:", err)
		os.Exit(1)
	}
}

func run() error {
	bin := os.Getenv("CLD_TARGET")
	if bin == "" {
		bin = "claude"
	}
	cmd := exec.Command(bin, os.Args[1:]...)
	cmd.Env = os.Environ()

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("start %s: %w", bin, err)
	}
	defer func() { _ = ptmx.Close() }()

	// Raw stdin so keystrokes pass straight through to Claude.
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err == nil {
		defer term.Restore(int(os.Stdin.Fd()), oldState)
	}

	cols, rows := 80, 24
	if c, r, e := term.GetSize(int(os.Stdin.Fd())); e == nil {
		cols, rows = c, r
	}

	id := strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	cwd, _ := os.Getwd()
	meta := agentfs.Meta{
		ID:        id,
		AgentPID:  os.Getpid(),
		ChildPID:  cmd.Process.Pid,
		Cwd:       cwd,
		Args:      os.Args[1:],
		Cols:      cols,
		Rows:      rows,
		StartedAt: time.Now().UnixMilli(),
	}
	if err := agentfs.WriteMeta(meta); err != nil {
		fmt.Fprintln(os.Stderr, "cld: could not register:", err)
	}
	if err := agentfs.EnsureFifo(id); err != nil {
		fmt.Fprintln(os.Stderr, "cld: no input channel:", err)
	}
	defer agentfs.Remove(id)

	// Restore the terminal and deregister even if we're killed.
	cleanup := func() {
		if oldState != nil {
			_ = term.Restore(int(os.Stdin.Fd()), oldState)
		}
		agentfs.Remove(id)
	}
	deathSig := make(chan os.Signal, 1)
	signal.Notify(deathSig, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
	go func() {
		<-deathSig
		cleanup()
		os.Exit(130)
	}()

	// Virtual terminal that reconstructs Claude's fullscreen UI into a grid.
	vt := vt10x.New(vt10x.WithSize(cols, rows))
	var vtmu sync.Mutex

	// reconcileSize keeps the PTY and VT at the right dimensions: cmanager's
	// requested size while it is viewing this session, otherwise our own
	// terminal. It is cheap and idempotent, so we can call it freely.
	var appliedCols, appliedRows int
	var wasRemote bool // last size came from cmanager (it was viewing us)
	reconcileSize := func() {
		c, r := cols, rows
		if tc, tr, e := term.GetSize(int(os.Stdin.Fd())); e == nil {
			c, r = tc, tr
		}
		remote := false
		if rc, rr, ok := agentfs.ReadResize(id); ok {
			c, r = rc, rr
			remote = true
		}
		if c <= 0 || r <= 0 {
			return
		}
		sizeChanged := c != appliedCols || r != appliedRows
		// Hand-back: cmanager was viewing us at its own geometry and just
		// detached. Claude was rendering for that foreign size, so this
		// terminal is littered with mis-wrapped, overlapping lines that
		// claude's repaint won't clear on its own (it only redraws its current
		// frame, not the stranded scrollback). Wipe screen + scrollback so the
		// SIGWINCH-driven repaint at our real size comes back clean.
		handBack := wasRemote && !remote
		if !sizeChanged && !handBack {
			return
		}
		appliedCols, appliedRows = c, r
		wasRemote = remote
		if sizeChanged {
			_ = pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(c), Rows: uint16(r)})
			vtmu.Lock()
			vt.Resize(c, r)
			vtmu.Unlock()
		}
		if handBack {
			_, _ = os.Stdout.WriteString("\x1b[H\x1b[2J\x1b[3J")
		}
	}
	reconcileSize() // size the PTY before Claude paints its first frame

	// React promptly to our own terminal being resized (cmanager-driven
	// resizes are picked up by the snapshot ticker below).
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			reconcileSize()
		}
	}()

	// dirty signals the snapshot goroutine that the screen likely changed.
	// Buffered to 1 so a burst of output coalesces into a single pending frame.
	dirty := make(chan struct{}, 1)
	markDirty := func() {
		select {
		case dirty <- struct{}{}:
		default:
		}
	}

	// PTY -> (our stdout + VT). This is the "tee".
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				os.Stdout.Write(buf[:n])
				vtmu.Lock()
				vt.Write(buf[:n])
				vtmu.Unlock()
				markDirty()
			}
			if err != nil {
				return
			}
		}
	}()

	// Snapshot the screen for cmanager. Rendering is driven by PTY output
	// (low latency) but capped to ~60fps; a slower heartbeat keeps the size
	// reconciled while idle. Writes are skipped when the frame is unchanged.
	stop := make(chan struct{})
	go func() {
		recon := time.NewTicker(reconcileInterval)
		defer recon.Stop()
		var last string
		var lastRender time.Time
		render := func() {
			reconcileSize() // pick up cmanager's requested size, or snap back on detach
			vtmu.Lock()
			s := renderColorScreen(vt)
			vtmu.Unlock()
			if s != last {
				last = s
				_ = agentfs.WriteScreen(id, s)
			}
			lastRender = time.Now()
		}
		for {
			select {
			case <-stop:
				return
			case <-recon.C:
				render()
				continue
			case <-dirty:
			}
			// Coalesce a burst of output into one frame and cap to ~60fps.
			if d := time.Since(lastRender); d < frameInterval {
				time.Sleep(frameInterval - d)
				select { // drain any signal that arrived while sleeping
				case <-dirty:
				default:
				}
			}
			render()
		}
	}()

	// Remote input from cmanager -> PTY.
	go injectRemoteInput(id, ptmx)

	// Local stdin -> PTY.
	go func() { _, _ = io.Copy(ptmx, os.Stdin) }()

	err = cmd.Wait()
	close(stop)
	return err
}

// injectRemoteInput reads from the input FIFO and writes to the PTY. Opening
// the FIFO O_RDWR keeps it open across multiple cmanager writers without EOF.
func injectRemoteInput(id string, ptmx io.Writer) {
	f, err := os.OpenFile(agentfs.InputPath(id), os.O_RDWR, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	buf := make([]byte, 4096)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			_, _ = ptmx.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}
