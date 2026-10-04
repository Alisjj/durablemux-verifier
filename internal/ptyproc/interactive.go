package ptyproc

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// Interact forwards a real terminal to the process while keeping a transcript.
// The input reader is joined before returning, so it cannot eat later prompts.
func (p *Process) Interact(input *os.File, output io.Writer) (int, error) {
	fd := int(input.Fd())
	original, err := unix.IoctlGetTermios(fd, ioctlGetTermios())
	if err != nil {
		return 0, fmt.Errorf("live commands require an interactive terminal: %w", err)
	}
	raw := *original
	raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	raw.Oflag &^= unix.OPOST
	raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8
	raw.Cc[unix.VMIN], raw.Cc[unix.VTIME] = 1, 0
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios(), &raw); err != nil {
		return 0, err
	}
	defer unix.IoctlSetTermios(fd, ioctlSetTermios(), original)
	resize := func() {
		if size, err := pty.GetsizeFull(input); err == nil {
			_ = p.SetSize(int(size.Rows), int(size.Cols))
		}
	}
	resize()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, unix.SIGWINCH, unix.SIGTERM, unix.SIGINT, unix.SIGHUP, unix.SIGQUIT)
	defer signal.Stop(signals)
	done, inputDone := make(chan struct{}), make(chan struct{})
	inputErrors := make(chan error, 1)
	go func() {
		defer close(inputDone)
		buf := make([]byte, 4096)
		for {
			select {
			case <-done:
				return
			default:
			}
			fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			n, err := unix.Poll(fds, 100)
			if err == unix.EINTR {
				continue
			}
			if err != nil {
				inputErrors <- err
				return
			}
			if n == 0 {
				continue
			}
			select {
			case <-done:
				return
			default:
			}
			n, err = input.Read(buf)
			if err == nil && n > 0 {
				err = p.Write(buf[:n])
			}
			if err != nil || n == 0 {
				if err == nil {
					err = io.EOF
				}
				inputErrors <- err
				return
			}
		}
	}()
	defer func() { close(done); <-inputDone }()
	for {
		if data := p.ReadSome(50 * time.Millisecond); len(data) > 0 {
			if _, err := output.Write(data); err != nil {
				return 0, err
			}
		}
		if rc := p.Poll(); rc != nil {
			// Drain the last output without displaying the transcript twice.
			until := time.Now().Add(100 * time.Millisecond)
			for !p.readEOF && time.Now().Before(until) {
				if data := p.ReadSome(10 * time.Millisecond); len(data) > 0 {
					if _, err := output.Write(data); err != nil {
						return 0, err
					}
				}
			}
			return *rc, nil
		}
		select {
		case sig := <-signals:
			if sig == unix.SIGWINCH {
				resize()
			} else {
				return 0, fmt.Errorf("live review interrupted by %s", sig)
			}
		case err := <-inputErrors:
			return 0, err
		default:
		}
	}
}
