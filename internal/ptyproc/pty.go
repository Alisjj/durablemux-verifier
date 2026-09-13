package ptyproc

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/creack/pty"
)

// Process wraps a child attached to a PTY master.
type Process struct {
	Cmd    *exec.Cmd
	Master *os.File
	Buffer []byte
	Pid    int
	rc     *int
}

// Start launches argv under a PTY sized rows x cols.
func Start(argv []string, cwd string, env []string, rows, cols int) (*Process, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("empty argv")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = cwd
	cmd.Env = env
	ws := &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)}
	master, err := pty.StartWithSize(cmd, ws)
	if err != nil {
		return nil, err
	}
	return &Process{Cmd: cmd, Master: master, Pid: cmd.Process.Pid}, nil
}

// SetSize resizes the PTY (also delivers SIGWINCH to the foreground pg).
func (p *Process) SetSize(rows, cols int) error {
	return pty.Setsize(p.Master, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
}

// Write sends bytes to the PTY master.
func (p *Process) Write(data []byte) error {
	_, err := p.Master.Write(data)
	return err
}

// ReadSome non-blocking-ish read with timeout; appends to Buffer.
func (p *Process) ReadSome(timeout time.Duration) []byte {
	type res struct {
		n   int
		b   []byte
		err error
	}
	ch := make(chan res, 1)
	go func() {
		buf := make([]byte, 65536)
		n, err := p.Master.Read(buf)
		ch <- res{n, buf[:max(n, 0)], err}
	}()
	select {
	case r := <-ch:
		if r.n > 0 {
			p.Buffer = append(p.Buffer, r.b...)
			return r.b
		}
		return nil
	case <-time.After(timeout):
		return nil
	}
}

// ReadUntil blocks until needle appears or timeout; returns full buffer.
func (p *Process) ReadUntil(needle []byte, timeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if bytes.Contains(p.Buffer, needle) {
			return append([]byte(nil), p.Buffer...), nil
		}
		if p.Poll() != nil {
			p.ReadSome(0)
			break
		}
		left := time.Until(deadline)
		if left > 100*time.Millisecond {
			left = 100 * time.Millisecond
		}
		if left < 0 {
			left = 0
		}
		p.ReadSome(left)
	}
	if !bytes.Contains(p.Buffer, needle) {
		tail := p.Buffer
		if len(tail) > 2000 {
			tail = tail[len(tail)-2000:]
		}
		return append([]byte(nil), p.Buffer...), fmt.Errorf("did not observe %q; output=%q", needle, tail)
	}
	return append([]byte(nil), p.Buffer...), nil
}

// Poll returns exit code or nil when running.
func (p *Process) Poll() *int {
	if p.rc != nil {
		return p.rc
	}
	// Non-blocking check via signal 0 + process state.
	if p.Cmd.ProcessState != nil {
		code := p.Cmd.ProcessState.ExitCode()
		p.rc = &code
		return p.rc
	}
	// Reap without blocking: use Wait in goroutine? Instead check with
	// a zero-timeout wait via channel cached on first call.
	select {
	case <-waitChan(p.Cmd):
		if p.Cmd.ProcessState != nil {
			code := p.Cmd.ProcessState.ExitCode()
			p.rc = &code
		} else {
			code := 0
			p.rc = &code
		}
		return p.rc
	default:
		return nil
	}
}

var waitCache = map[*exec.Cmd]chan struct{}{}

func waitChan(cmd *exec.Cmd) chan struct{} {
	// NOTE: single-flight per *exec.Cmd; fine for verifier lifetimes.
	if ch, ok := waitCache[cmd]; ok {
		return ch
	}
	ch := make(chan struct{})
	waitCache[cmd] = ch
	go func() {
		_ = cmd.Wait()
		close(ch)
	}()
	return ch
}

// Wait blocks until exit or timeout.
func (p *Process) Wait(timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if rc := p.Poll(); rc != nil {
			return *rc, nil
		}
		p.ReadSome(50 * time.Millisecond)
	}
	return 0, fmt.Errorf("process did not exit: %v", p.Cmd.Args)
}

// Terminate sends SIGTERM then SIGKILL and closes the master.
func (p *Process) Terminate() {
	if p.Poll() != nil {
		p.Close()
		return
	}
	if p.Cmd.Process != nil {
		_ = p.Cmd.Process.Signal(os.Interrupt)
		if _, err := p.Wait(500 * time.Millisecond); err == nil {
			p.Close()
			return
		}
		_ = p.Cmd.Process.Kill()
	}
	p.Close()
}

// LFlag reads the PTY termios local flags (for ICANON/ECHO checks).
// Returns 0 with error on platforms where termios is unavailable;
// callers should treat error as skip, not fail.
func (p *Process) LFlag() (uint64, error) {
	return lflagOf(p.Master.Fd())
}

// Close closes the master fd.
func (p *Process) Close() {
	if p.Master != nil {
		_ = p.Master.Close()
		p.Master = nil
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
