package ptyproc

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"
)

// Process wraps a child attached to a PTY master.
type Process struct {
	Cmd    *exec.Cmd
	Master *os.File
	Buffer []byte
	Pid    int

	readCh    chan []byte
	readDone  chan struct{}
	waitCh    chan int
	closeOnce sync.Once
	mu        sync.Mutex
	rc        *int
	readEOF   bool
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
	p := &Process{
		Cmd: cmd, Master: master, Pid: cmd.Process.Pid,
		readCh: make(chan []byte, 1), readDone: make(chan struct{}),
		waitCh: make(chan int, 1),
	}
	go p.readLoop(master)
	go func() {
		err := cmd.Wait()
		code := 0
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				code = -1
			}
		}
		p.waitCh <- code
	}()
	return p, nil
}

// readLoop is the sole reader of the PTY. A timed-out ReadSome must not leave
// an abandoned goroutine that can consume and discard future output.
func (p *Process) readLoop(master *os.File) {
	defer close(p.readCh)
	for {
		buf := make([]byte, 65536)
		n, err := master.Read(buf)
		if n > 0 {
			data := append([]byte(nil), buf[:n]...)
			select {
			case p.readCh <- data:
			case <-p.readDone:
				return
			}
		}
		if err != nil {
			return
		}
	}
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
	data, _ := p.readSome(timeout)
	return data
}

func (p *Process) readSome(timeout time.Duration) ([]byte, bool) {
	if p.readEOF {
		return nil, false
	}
	consume := func(data []byte, ok bool) ([]byte, bool) {
		if !ok {
			p.readEOF = true
			return nil, false
		}
		p.Buffer = append(p.Buffer, data...)
		return data, true
	}
	if timeout <= 0 {
		select {
		case data, ok := <-p.readCh:
			return consume(data, ok)
		default:
			return nil, true
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case data, ok := <-p.readCh:
		return consume(data, ok)
	case <-timer.C:
		return nil, true
	}
}

// ReadUntil blocks until needle appears or timeout; returns full buffer.
func (p *Process) ReadUntil(needle []byte, timeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if bytes.Contains(p.Buffer, needle) {
			return append([]byte(nil), p.Buffer...), nil
		}
		left := time.Until(deadline)
		if left > 100*time.Millisecond {
			left = 100 * time.Millisecond
		}
		if left < 0 {
			left = 0
		}
		if _, open := p.readSome(left); !open {
			break
		}
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
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.rc != nil {
		return p.rc
	}
	select {
	case code := <-p.waitCh:
		p.rc = &code
		return p.rc
	default:
		return nil
	}
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
	p.closeOnce.Do(func() {
		close(p.readDone)
		if p.Master != nil {
			_ = p.Master.Close()
		}
	})
}
