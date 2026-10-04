package checks

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Alisjj/durablemux-verifier/internal/model"
	"github.com/Alisjj/durablemux-verifier/internal/runner"
	"github.com/Alisjj/durablemux-verifier/internal/util"

	"golang.org/x/sys/unix"
)

// CheckFn is a group of checks for one stage_ key.
type CheckFn func(*runner.Context) []model.CheckResult

func single(ctx *runner.Context, name string, fn func() (bool, string, []byte, []byte)) model.CheckResult {
	start := time.Now()
	passed, detail, stdout, stderr := false, "", []byte{}, []byte{}
	func() {
		defer func() {
			if r := recover(); r != nil {
				passed = false
				detail = fmt.Sprintf("%v", r)
			}
		}()
		passed, detail, stdout, stderr = fn()
	}()
	return ctx.Result(name, passed, detail, start, stdout, stderr)
}

// ---------- stage 1 ----------

func stage1(ctx *runner.Context) []model.CheckResult {
	var out []model.CheckResult
	out = append(out, single(ctx, "version prints exactly one line", func() (bool, string, []byte, []byte) {
		r := ctx.RunAction("version", nil, nil, nil, 0)
		lines := []string{}
		for _, l := range strings.Split(string(r.Stdout), "\n") {
			if strings.TrimSpace(l) != "" {
				lines = append(lines, l)
			}
		}
		ok := r.ReturnCode == 0 && len(lines) == 1
		return ok, fmt.Sprintf("exit=%d, non-empty lines=%d", r.ReturnCode, len(lines)), r.Stdout, r.Stderr
	}))
	out = append(out, single(ctx, "help is available", func() (bool, string, []byte, []byte) {
		r := ctx.RunAction("help", nil, nil, nil, 0)
		text := strings.ToLower(string(r.Stdout))
		ok := r.ReturnCode == 0 && strings.TrimSpace(text) != "" && (strings.Contains(text, "version") || strings.Contains(text, "help"))
		return ok, fmt.Sprintf("exit=%d", r.ReturnCode), r.Stdout, r.Stderr
	}))
	out = append(out, single(ctx, "unknown command separates diagnostics", func() (bool, string, []byte, []byte) {
		r := ctx.Run([]string{ctx.Binary, "__definitely_unknown_command__"}, nil, 0, "")
		ok := r.Failed() && len(bytes.TrimSpace(r.Stdout)) == 0 && len(bytes.TrimSpace(r.Stderr)) > 0
		return ok, fmt.Sprintf("exit=%d; stdout must be empty and stderr non-empty", r.ReturnCode), r.Stdout, r.Stderr
	}))
	out = append(out, single(ctx, "works outside repository root", func() (bool, string, []byte, []byte) {
		dir, err := os.MkdirTemp("", "dmux-outside-")
		if err != nil {
			return false, err.Error(), nil, nil
		}
		defer os.RemoveAll(dir)
		argv, err := ctx.Command("version", nil)
		if err != nil {
			return false, err.Error(), nil, nil
		}
		r := ctx.Run(argv, nil, 0, dir)
		return r.ReturnCode == 0, fmt.Sprintf("exit=%d", r.ReturnCode), r.Stdout, r.Stderr
	}))
	return out
}

// ---------- stage 2 ----------

func stage2(ctx *runner.Context) []model.CheckResult {
	var out []model.CheckResult
	printf := "/usr/bin/printf"
	if _, err := os.Stat(printf); err != nil {
		printf = "printf"
	}
	out = append(out, single(ctx, "argument array is not shell-interpreted", func() (bool, string, []byte, []byte) {
		args := []string{"hello world|$HOME>*"}
		extra := append([]string{printf, "%s"}, args...)
		r := ctx.RunAction("run", nil, extra, nil, 0)
		return r.ReturnCode == 0 && bytes.Equal(r.Stdout, []byte(args[0])), fmt.Sprintf("exit=%d; expected literal shell characters", r.ReturnCode), r.Stdout, r.Stderr
	}))
	out = append(out, single(ctx, "absolute executable path works", func() (bool, string, []byte, []byte) {
		r := ctx.RunAction("run", nil, []string{printf, "absolute-ok"}, nil, 0)
		return r.ReturnCode == 0 && bytes.Equal(r.Stdout, []byte("absolute-ok")), fmt.Sprintf("exit=%d", r.ReturnCode), r.Stdout, r.Stderr
	}))
	out = append(out, single(ctx, "missing executable fails", func() (bool, string, []byte, []byte) {
		r := ctx.RunAction("run", nil, []string{"/definitely/missing/dmux-verifier-executable"}, nil, 0)
		return r.Failed(), fmt.Sprintf("exit=%d; timeout=%v; runner error=%s", r.ReturnCode, r.TimedOut, r.Error), r.Stdout, r.Stderr
	}))
	return out
}

// ---------- stage 3 ----------

func stage3(ctx *runner.Context) []model.CheckResult {
	return []model.CheckResult{single(ctx, "stdin/stdout/stderr and exit status are transparent", func() (bool, string, []byte, []byte) {
		script := "read line; printf 'OUT:%s' \"$line\"; printf 'ERR:%s' \"$line\" >&2; exit 7"
		r := ctx.RunAction("run", nil, []string{"sh", "-c", script}, []byte("hello\n"), 0)
		ok := r.ReturnCode == 7 && bytes.Equal(r.Stdout, []byte("OUT:hello")) && bytes.Equal(r.Stderr, []byte("ERR:hello"))
		return ok, fmt.Sprintf("exit=%d; expected child exit 7 and transparent streams", r.ReturnCode), r.Stdout, r.Stderr
	})}
}

// ---------- stage 5 ----------

var devRE = regexp.MustCompile(`/dev/(pts/\d+|tty\S*)`)

func stage5(ctx *runner.Context) []model.CheckResult {
	var out []model.CheckResult
	out = append(out, single(ctx, "child is connected to a PTY", func() (bool, string, []byte, []byte) {
		p, err := ctx.SpawnPTY("run", nil, []string{"sh", "-c", "printf '__DMUX_TTY__'; tty"}, 24, 80)
		if err != nil {
			return false, err.Error(), nil, nil
		}
		defer p.Terminate()
		deadline := time.Now().Add(ctx.Timeout)
		for p.Poll() == nil && time.Now().Before(deadline) {
			p.ReadSome(100 * time.Millisecond)
		}
		rcPtr := p.Poll()
		rc := -1
		if rcPtr != nil {
			rc = *rcPtr
		} else {
			var err error
			rc, err = p.Wait(time.Second)
			if err != nil {
				return false, "timeout waiting for tty", append([]byte(nil), p.Buffer...), nil
			}
		}
		data := append([]byte(nil), p.Buffer...)
		text := strings.TrimSpace(string(data))
		ok := rc == 0 && !strings.Contains(text, "not a tty") && devRE.MatchString(text) && separateTTY(p, data)
		return ok, fmt.Sprintf("reported terminal=%q; harness terminal=%s; child must use a separate PTY", text, p.OuterTTY), data, nil
	}))
	out = append(out, single(ctx, "all standard streams are TTYs", func() (bool, string, []byte, []byte) {
		code := "import os; print('__DMUX_TTY__'+os.ttyname(0)); print(int(os.isatty(0)), int(os.isatty(1)), int(os.isatty(2)),flush=True)"
		p, err := ctx.SpawnPTY("run", nil, []string{"python3", "-c", code}, 24, 80)
		if err != nil {
			return false, err.Error(), nil, nil
		}
		defer p.Terminate()
		data, err := p.ReadUntil([]byte("1 1 1"), ctx.Timeout)
		if err != nil {
			return false, "stdin, stdout and stderr should all be terminal devices", data, nil
		}
		rc, err := p.Wait(ctx.Timeout)
		if err != nil {
			return false, err.Error(), data, nil
		}
		return rc == 0 && separateTTY(p, data), "all streams must be terminals on a separate child PTY", data, nil
	}))
	out = append(out, single(ctx, "TTY probe distinguishes pipes from terminals", func() (bool, string, []byte, []byte) {
		r := ctx.Run([]string{"python3", "-c", "import os; print(int(os.isatty(0)),int(os.isatty(1)))"}, nil, 0, "")
		return r.Succeeded() && bytes.Equal(bytes.TrimSpace(r.Stdout), []byte("0 0")), "same probe reports non-terminal streams through pipes", r.Stdout, r.Stderr
	}))
	return out
}

// ---------- stage 6 ----------

func stage6(ctx *runner.Context) []model.CheckResult {
	return []model.CheckResult{single(ctx, "interactive shell accepts commands", func() (bool, string, []byte, []byte) {
		p, err := spawnShell(ctx)
		if err != nil {
			return false, err.Error(), nil, nil
		}
		defer p.Terminate()
		if err := p.Write([]byte("python3 -c \"import os; print('__DMUX_TTY__'+os.ttyname(0))\"\n")); err != nil {
			return false, err.Error(), nil, nil
		}
		data, err := shellChallenge(p, ctx.Timeout)
		if err != nil {
			return false, err.Error(), data, nil
		}
		if !separateTTY(p, data) {
			return false, "interactive shell must run on its own PTY", data, nil
		}
		if err := p.Write([]byte("exit\n")); err != nil {
			return false, err.Error(), data, nil
		}
		rc, err := p.Wait(ctx.Timeout)
		if err != nil {
			return false, err.Error(), data, nil
		}
		return rc == 0, fmt.Sprintf("interactive shell marker observed; exit=%d", rc), data, nil
	})}
}

// ---------- stage 7 ----------

func stage7(ctx *runner.Context) []model.CheckResult {
	return []model.CheckResult{single(ctx, "client terminal is switched to immediate/raw input", func() (bool, string, []byte, []byte) {
		code := "import os,tty,termios; fd=0; old=termios.tcgetattr(fd); tty.setraw(fd); print('__DMUX_TTY__'+os.ttyname(0),flush=True); print('__READY__',flush=True); b=os.read(fd,1); os.write(1,b'__KEY__'+b); termios.tcsetattr(fd,termios.TCSANOW,old)"
		p, err := ctx.SpawnPTY("run", nil, []string{"python3", "-c", code}, 24, 80)
		if err != nil {
			return false, err.Error(), nil, nil
		}
		defer p.Terminate()
		ready, err := p.ReadUntil([]byte("__READY__"), ctx.Timeout)
		if err != nil || !separateTTY(p, ready) {
			return false, "child readiness and a separate PTY are required", ready, nil
		}
		flags, err := p.LFlag()
		if err != nil || flags&uint64(unix.ICANON|unix.ECHO) != 0 {
			return false, fmt.Sprintf("client terminal must disable ICANON/ECHO: flags=%x; error=%v", flags, err), ready, nil
		}
		if err := p.Write([]byte("Z")); err != nil {
			return false, err.Error(), ready, nil
		}
		data, err := p.ReadUntil([]byte("__KEY__Z"), 1500*time.Millisecond)
		if err != nil {
			return false, "single byte reached child without Enter", data, nil
		}
		rc, err := p.Wait(ctx.Timeout)
		if err != nil {
			return false, err.Error(), data, nil
		}
		return rc == 0 && bytes.Count(p.Buffer[len(ready):], []byte("Z")) == 1, "single byte reached child without Enter or double echo", append([]byte(nil), p.Buffer...), nil
	})}
}

// ---------- stage 8 ----------

func stage8(ctx *runner.Context) []model.CheckResult {
	var results []model.CheckResult
	checkRestore := func(name string, extra []string, terminate bool) model.CheckResult {
		return single(ctx, name, func() (bool, string, []byte, []byte) {
			p, err := ctx.SpawnPTY("run", nil, extra, 24, 80)
			if err != nil {
				return false, err.Error(), nil, nil
			}
			defer p.Close()
			if terminate {
				data, err := p.ReadUntil([]byte("__READY__"), ctx.Timeout)
				if err != nil || !separateTTY(p, data) {
					p.Terminate()
					return false, "separate child PTY must be ready before termination", data, nil
				}
				flags, err := p.LFlag()
				if err != nil || flags&uint64(unix.ICANON|unix.ECHO) != 0 {
					p.Terminate()
					return false, "client must enter raw mode before testing signal cleanup", data, nil
				}
				if err := p.Cmd.Process.Signal(unix.SIGTERM); err != nil {
					return false, err.Error(), data, nil
				}
			}
			rc, err := p.Wait(ctx.Timeout)
			if err != nil {
				p.Terminate()
				return false, err.Error(), append([]byte(nil), p.Buffer...), nil
			}
			data := append([]byte(nil), p.Buffer...)
			restored, lerr := p.TerminalRestored()
			if lerr != nil {
				return false, "cannot measure terminal restoration: " + lerr.Error(), data, nil
			}
			ok := restored
			wantFail := len(extra) > 0 && strings.Contains(strings.Join(extra, " "), "missing")
			if wantFail {
				ok = rc != 0 && ok
			} else if !terminate {
				ok = rc == 0 && ok
			}
			return ok, fmt.Sprintf("exit=%d; original terminal configuration restored=%v", rc, restored), data, nil
		})
	}
	results = append(results, checkRestore("terminal state restored after normal exit", []string{"sh", "-c", "sleep 0.2"}, false))
	results = append(results, checkRestore("terminal state restored after command-start failure", []string{"/definitely/missing/dmux-command"}, false))
	results = append(results, checkRestore("terminal state restored after SIGTERM", []string{"python3", "-c", "import os,time; print('__DMUX_TTY__'+os.ttyname(0),flush=True); print('__READY__',flush=True); time.sleep(60)"}, true))
	return results
}

// ---------- stage 9 ----------

func stage9(ctx *runner.Context) []model.CheckResult {
	return []model.CheckResult{single(ctx, "initial terminal size is propagated", func() (bool, string, []byte, []byte) {
		p, err := ctx.SpawnPTY("run", nil, []string{"sh", "-c", "printf '__DMUX_TTY__'; tty; stty size"}, 37, 101)
		if err != nil {
			return false, err.Error(), nil, nil
		}
		defer p.Terminate()
		data, err := p.ReadUntil([]byte("37 101"), ctx.Timeout)
		if err != nil {
			return false, fmt.Sprintf("expected 37x101; %v", err), data, nil
		}
		rc, err := p.Wait(ctx.Timeout)
		if err != nil {
			return false, err.Error(), data, nil
		}
		return rc == 0 && separateTTY(p, data), fmt.Sprintf("expected 37x101 on a separate child PTY; exit=%d", rc), data, nil
	})}
}

// ---------- stage 10 ----------

func stage10(ctx *runner.Context) []model.CheckResult {
	return []model.CheckResult{single(ctx, "dynamic terminal resize is propagated", func() (bool, string, []byte, []byte) {
		code := "import os,fcntl,signal,struct,termios,time; " +
			"size=lambda: struct.unpack('HHHH',fcntl.ioctl(0,termios.TIOCGWINSZ,struct.pack('HHHH',0,0,0,0)))[:2]; " +
			"h=lambda a,b: os.write(1,('__SIZE__%dx%d\\n'%size()).encode()); " +
			"signal.signal(signal.SIGWINCH,h); print('__DMUX_TTY__'+os.ttyname(0),flush=True); print('__READY__',flush=True); " +
			"time.sleep(10)"
		p, err := ctx.SpawnPTY("run", nil, []string{"python3", "-c", code}, 24, 80)
		if err != nil {
			return false, err.Error(), nil, nil
		}
		defer p.Terminate()
		if data, err := p.ReadUntil([]byte("__READY__"), ctx.Timeout); err != nil || !separateTTY(p, data) {
			return false, "resize probe must start on a separate child PTY", data, nil
		}
		for _, size := range [][2]int{{41, 109}, {19, 73}, {53, 127}} {
			if err := p.SetSize(size[0], size[1]); err != nil {
				return false, err.Error(), append([]byte(nil), p.Buffer...), nil
			}
			if data, err := p.ReadUntil([]byte(fmt.Sprintf("__SIZE__%dx%d", size[0], size[1])), ctx.Timeout); err != nil {
				return false, err.Error(), data, nil
			}
		}
		for i := 0; i < 20; i++ {
			if err := p.SetSize(30+i, 90+i); err != nil {
				return false, err.Error(), append([]byte(nil), p.Buffer...), nil
			}
		}
		data, err := p.ReadUntil([]byte("__SIZE__49x109"), ctx.Timeout)
		return err == nil && p.Poll() == nil, fmt.Sprintf("repeated and rapid resize; final dimensions reached child; error=%v", err), data, nil
	})}
}

// ---------- stage 11 ----------

func stage11(ctx *runner.Context) []model.CheckResult {
	var results []model.CheckResult
	results = append(results, single(ctx, "Ctrl+C preserves interactive shell", func() (bool, string, []byte, []byte) {
		p, err := spawnShell(ctx)
		if err != nil {
			return false, err.Error(), nil, nil
		}
		defer p.Terminate()
		_ = p.Write([]byte("python3 -c \"import os; print('__DMUX_TTY__'+os.ttyname(0))\"\n"))
		if data, err := shellChallenge(p, ctx.Timeout); err != nil || !separateTTY(p, data) {
			return false, "interactive shell must be ready on a separate PTY", data, nil
		}
		if _, err := startForegroundJob(ctx, p); err != nil {
			return false, err.Error(), append([]byte(nil), p.Buffer...), nil
		}
		_ = p.Write([]byte{0x03})
		data, err := shellChallenge(p, 3*time.Second)
		if err != nil {
			return false, "Ctrl+C interrupted foreground command and shell survived: " + err.Error(), data, nil
		}
		_ = p.Write([]byte("exit\n"))
		rc, err := p.Wait(ctx.Timeout)
		if err != nil {
			return false, err.Error(), data, nil
		}
		return rc == 0, "Ctrl+C interrupted foreground command and shell survived", data, nil
	}))
	results = append(results, single(ctx, "Ctrl+Z supports shell job control", func() (bool, string, []byte, []byte) {
		p, err := spawnShell(ctx)
		if err != nil {
			return false, err.Error(), nil, nil
		}
		defer p.Terminate()
		_ = p.Write([]byte("python3 -c \"import os; print('__DMUX_TTY__'+os.ttyname(0))\"\n"))
		if data, err := shellChallenge(p, ctx.Timeout); err != nil || !separateTTY(p, data) {
			return false, "interactive shell must be ready on a separate PTY", data, nil
		}
		job, err := startForegroundJob(ctx, p)
		if err != nil {
			return false, err.Error(), append([]byte(nil), p.Buffer...), nil
		}
		_ = p.Write([]byte{0x1a})
		_ = p.Write([]byte("jobs\n"))
		data, err := p.ReadUntil([]byte("Stopped"), 3*time.Second)
		if err != nil {
			return false, "Ctrl+Z created a stopped job visible to the shell: " + err.Error(), data, nil
		}
		_ = p.Write([]byte("bg %1\njobs\n"))
		if data, err = p.ReadUntil([]byte("Running"), ctx.Timeout); err != nil {
			return false, "bg must resume the stopped job: " + err.Error(), data, nil
		}
		if !waitForeground(job, ctx.Timeout, false) {
			return false, "bg did not resume the job outside the foreground process group", data, nil
		}
		_ = p.Write([]byte("fg %1\n"))
		if !waitForeground(job, ctx.Timeout, true) {
			return false, "fg did not return the job to the foreground process group", data, nil
		}
		_ = p.Write([]byte{0x03})
		if data, err = shellChallenge(p, ctx.Timeout); err != nil {
			return false, "shell must remain interactive after fg and Ctrl+C: " + err.Error(), data, nil
		}
		_ = p.Write([]byte("exit\n"))
		rc, err := p.Wait(ctx.Timeout)
		return err == nil && rc == 0, fmt.Sprintf("Ctrl+Z, jobs, bg, fg, Ctrl+C and subsequent interaction; exit=%d; error=%v", rc, err), data, nil
	}))
	return results
}

// ---------- stage 12 ----------

func stage12(ctx *runner.Context) []model.CheckResult {
	var results []model.CheckResult
	name := fmt.Sprintf("verify-%d-identity", os.Getpid())
	ctx.Sessions[name] = true
	results = append(results, single(ctx, "duplicate active session name is rejected", func() (bool, string, []byte, []byte) {
		for i, activeName := range []string{name, name + "-other"} {
			pidfile := filepath.Join(ctx.TempDir, fmt.Sprintf("identity-%d.pid", i))
			created := ctx.NewSession(activeName, writePIDCommand(pidfile, false), ctx.Timeout)
			if !created.Succeeded() {
				return false, fmt.Sprintf("valid session %s must be created successfully; exit=%d", activeName, created.ReturnCode), created.Stdout, created.Stderr
			}
			if _, alive := livePID(pidfile, 2*time.Second); !alive {
				return false, "valid session command did not become live", created.Stdout, created.Stderr
			}
		}
		second := ctx.RunAction("new", map[string]string{"name": name}, []string{"sleep", "30"}, nil, 2*time.Second)
		ok := second.Failed() && len(bytes.TrimSpace(second.Stderr)) > 0
		return ok, fmt.Sprintf("two valid sessions created; duplicate exit=%d; timeout=%v", second.ReturnCode, second.TimedOut), second.Stdout, second.Stderr
	}))
	results = append(results, single(ctx, "path-like and excessive names are rejected", func() (bool, string, []byte, []byte) {
		bad := []string{"", "../escape", "/absolute", "a/b", strings.Repeat("x", 300)}
		var details []string
		allBad := true
		for _, v := range bad {
			r := ctx.RunAction("new", map[string]string{"name": v}, []string{"true"}, nil, 2*time.Second)
			disp := v
			if len(disp) > 20 {
				disp = disp[:20]
			}
			details = append(details, fmt.Sprintf("%q:%d", disp, r.ReturnCode))
			allBad = allBad && r.Failed() && len(bytes.TrimSpace(r.Stderr)) > 0
		}
		return allBad, strings.Join(details, "; "), nil, nil
	}))
	return results
}

func writePIDCommand(pidfile string, background bool) []string {
	q := "'" + strings.ReplaceAll(pidfile, "'", "'\\''") + "'"
	if background {
		return []string{"sh", "-c", fmt.Sprintf("echo $$ > %s; sleep 60 & echo $! > %s.child; wait", q, q)}
	}
	return []string{"sh", "-c", fmt.Sprintf("echo $$ > %s; sleep 60", q)}
}

// ---------- stage 13 ----------

func stage13(ctx *runner.Context) []model.CheckResult {
	return []model.CheckResult{single(ctx, "session survives creating CLI", func() (bool, string, []byte, []byte) {
		name := fmt.Sprintf("verify-%d-durable", os.Getpid())
		pidfile := filepath.Join(ctx.TempDir, "session.pid")
		r := ctx.NewSession(name, writePIDCommand(pidfile, false), 3*time.Second)
		pid, alive := livePID(pidfile, 2*time.Second)
		return r.Succeeded() && alive, fmt.Sprintf("new exit=%d; pid=%d; alive=%v", r.ReturnCode, pid, alive), r.Stdout, r.Stderr
	})}
}

// ---------- stage 14 ----------

func stage14(ctx *runner.Context) []model.CheckResult {
	return []model.CheckResult{single(ctx, "attach supports interaction and client crash isolation", func() (bool, string, []byte, []byte) {
		name := fmt.Sprintf("verify-%d-attach", os.Getpid())
		pidfile := filepath.Join(ctx.TempDir, "attach.pid")
		q := "'" + strings.ReplaceAll(pidfile, "'", "'\\''") + "'"
		created := ctx.NewSession(name, []string{"sh", "-c", fmt.Sprintf("echo $$ > %s; exec sh", q)}, 3*time.Second)
		if created.ReturnCode != 0 || !util.WaitUntil(func() bool {
			_, err := os.Stat(pidfile)
			return err == nil
		}, 2*time.Second) {
			return false, "failed to create attachable session", created.Stdout, created.Stderr
		}
		shellPID, alive := livePID(pidfile, 2*time.Second)
		if !alive {
			return false, "session shell must be live before attachment", created.Stdout, created.Stderr
		}
		p, err := ctx.SpawnPTY("attach", map[string]string{"name": name}, nil, 24, 80)
		if err != nil {
			return false, err.Error(), nil, nil
		}
		defer p.Terminate()
		data, err := shellChallenge(p, ctx.Timeout)
		if err != nil {
			return false, err.Error(), data, nil
		}
		if p.Poll() != nil {
			return false, "attach client exited before the crash-isolation test", data, nil
		}
		if err := p.Cmd.Process.Signal(unix.SIGKILL); err != nil {
			return false, err.Error(), data, nil
		}
		if _, err := p.Wait(time.Second); err != nil || !util.ProcessAlive(shellPID) {
			return false, "shell must survive confirmed client SIGKILL", data, nil
		}
		reconnected, err := ctx.SpawnPTY("attach", map[string]string{"name": name}, nil, 24, 80)
		if err != nil {
			return false, err.Error(), data, nil
		}
		defer reconnected.Terminate()
		data, err = shellChallenge(reconnected, ctx.Timeout)
		return err == nil && util.ProcessAlive(shellPID), fmt.Sprintf("executed challenge before and after client crash; shell PID=%d; error=%v", shellPID, err), data, nil
	})}
}

// ---------- stage 15 ----------

func stage15(ctx *runner.Context) []model.CheckResult {
	return []model.CheckResult{single(ctx, "detach preserves the shell and foreground job, and allows reattach", func() (bool, string, []byte, []byte) {
		return detachProbe(ctx)
	})}
}

// ---------- JSON helpers ----------

func sessionsFromJSON(ctx *runner.Context, payload any) ([]map[string]any, error) {
	if arr, ok := payload.([]any); ok {
		var out []map[string]any
		for _, e := range arr {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			} else {
				return nil, fmt.Errorf("every session array entry must be an object")
			}
		}
		return out, nil
	}
	if m, ok := payload.(map[string]any); ok {
		aliases := util.StringAliases(ctx.Config, "json_fields", "sessions_container")
		for _, k := range aliases {
			if v, ok := m[k].([]any); ok {
				var out []map[string]any
				for _, e := range v {
					if em, ok := e.(map[string]any); ok {
						out = append(out, em)
					} else {
						return nil, fmt.Errorf("every session array entry must be an object")
					}
				}
				return out, nil
			}
		}
	}
	return nil, fmt.Errorf("list --json must return a JSON array or an object containing a sessions/items/data array")
}

func fieldAliases(ctx *runner.Context, field string) []string {
	return util.StringAliases(ctx.Config, "json_fields", field)
}

// ---------- stage 16 ----------

func stage16(ctx *runner.Context) []model.CheckResult {
	var out []model.CheckResult
	out = append(out, single(ctx, "empty runtime lists zero sessions as valid JSON", func() (bool, string, []byte, []byte) {
		r := ctx.RunAction("list", nil, nil, nil, 0)
		if r.ReturnCode != 0 {
			return false, fmt.Sprintf("exit=%d on empty runtime", r.ReturnCode), r.Stdout, r.Stderr
		}
		payload, err := util.ParseJSONOutput(string(r.Stdout))
		if err != nil {
			return false, "invalid JSON: " + err.Error(), r.Stdout, r.Stderr
		}
		sessions, err := sessionsFromJSON(ctx, payload)
		if err != nil {
			return false, err.Error(), r.Stdout, r.Stderr
		}
		return len(sessions) == 0, fmt.Sprintf("sessions=%d, want 0 on fresh runtime", len(sessions)), r.Stdout, r.Stderr
	}))
	out = append(out, single(ctx, "machine-readable session listing exposes required fields", func() (bool, string, []byte, []byte) {
		names := []string{fmt.Sprintf("verify-%d-list-a", os.Getpid()), fmt.Sprintf("verify-%d-list-b", os.Getpid())}
		for _, name := range names {
			r := ctx.NewSession(name, []string{"sleep", "60"}, 3*time.Second)
			if r.ReturnCode != 0 {
				return false, fmt.Sprintf("could not create %s", name), r.Stdout, r.Stderr
			}
		}
		r := ctx.RunAction("list", nil, nil, nil, 0)
		payload, err := util.ParseJSONOutput(string(r.Stdout))
		if err != nil {
			return false, "invalid JSON: " + err.Error(), r.Stdout, r.Stderr
		}
		sessions, err := sessionsFromJSON(ctx, payload)
		if err != nil {
			return false, err.Error(), r.Stdout, r.Stderr
		}
		found := map[string]map[string]any{}
		for _, item := range sessions {
			if v := util.GetAny(item, fieldAliases(ctx, "name")); v != nil {
				found[fmt.Sprintf("%v", v)] = item
			}
		}
		required := []string{"id", "name", "command", "created_at", "attached_clients", "state"}
		var missingNames, missingFields []string
		for _, name := range names {
			if _, ok := found[name]; !ok {
				missingNames = append(missingNames, name)
			}
		}
		for _, name := range names {
			item := found[name]
			if item == nil {
				continue
			}
			for _, f := range required {
				if util.GetAny(item, fieldAliases(ctx, f)) == nil {
					missingFields = append(missingFields, name+"."+f)
				}
			}
			if err := validateLiveSession(ctx, item, name); err != nil {
				missingFields = append(missingFields, err.Error())
			}
		}
		if len(sessions) != len(names) {
			missingFields = append(missingFields, "unexpected or duplicate session entries")
		}
		ids := map[string]bool{}
		for _, item := range sessions {
			id := fmt.Sprint(util.GetAny(item, fieldAliases(ctx, "id")))
			if ids[id] {
				missingFields = append(missingFields, "duplicate stable IDs")
			}
			ids[id] = true
		}
		again := ctx.RunAction("list", nil, nil, nil, 0)
		repeated, err := util.ParseJSONOutput(string(again.Stdout))
		if err != nil || !again.Succeeded() {
			missingFields = append(missingFields, "repeat list failed or returned invalid JSON")
		} else if rows, err := sessionsFromJSON(ctx, repeated); err != nil || !stableSessionIDs(ctx, found, rows) {
			missingFields = append(missingFields, "session IDs changed between listings")
		}
		ok := r.ReturnCode == 0 && len(missingNames) == 0 && len(missingFields) == 0
		return ok, fmt.Sprintf("missing sessions=%v; missing fields=%v", missingNames, missingFields), r.Stdout, r.Stderr
	}))
	return out
}

// ---------- stage 17 ----------

func stage17(ctx *runner.Context) []model.CheckResult {
	return []model.CheckResult{single(ctx, "kill terminates the complete managed process tree", func() (bool, string, []byte, []byte) {
		name := fmt.Sprintf("verify-%d-kill", os.Getpid())
		pidfile := filepath.Join(ctx.TempDir, "kill.pid")
		created := ctx.NewSession(name, writePIDCommand(pidfile, true), 3*time.Second)
		childFile := pidfile + ".child"
		if created.ReturnCode != 0 || !util.WaitUntil(func() bool {
			_, e1 := os.Stat(pidfile)
			_, e2 := os.Stat(childFile)
			return e1 == nil && e2 == nil
		}, 2*time.Second) {
			return false, "failed to create process tree", created.Stdout, created.Stderr
		}
		parent, parentAlive := livePID(pidfile, ctx.Timeout)
		child, childAlive := livePID(childFile, ctx.Timeout)
		if !parentAlive || !childAlive || parent == child {
			return false, "distinct parent and child PIDs must both be live immediately before kill", nil, nil
		}
		first := ctx.RunAction("kill", map[string]string{"name": name}, nil, nil, 4*time.Second)
		dead := util.WaitUntil(func() bool {
			return !util.ProcessAlive(parent) && !util.ProcessAlive(child)
		}, 3*time.Second)
		second := ctx.RunAction("kill", map[string]string{"name": name}, nil, nil, 2*time.Second)
		repeatedOK := second.Succeeded() || (second.Failed() && (second.ReturnCode == 1 || second.ReturnCode == 2) && len(bytes.TrimSpace(second.Stderr)) > 0)
		ok := first.Succeeded() && dead && repeatedOK
		so := append(append([]byte{}, first.Stdout...), second.Stdout...)
		se := append(append([]byte{}, first.Stderr...), second.Stderr...)
		return ok, fmt.Sprintf("parent=%d, child=%d, both dead=%v, repeat exit=%d", parent, child, dead, second.ReturnCode), so, se
	})}
}

// ---------- stage 20 ----------

func stage20(ctx *runner.Context) []model.CheckResult {
	return []model.CheckResult{single(ctx, "concurrent first clients converge on a usable coordinator", func() (bool, string, []byte, []byte) {
		var wg sync.WaitGroup
		results := make([]runner.CommandOutput, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				results[idx] = ctx.RunAction("list", nil, nil, nil, 5*time.Second)
			}(i)
		}
		wg.Wait()
		good := 0
		var se bytes.Buffer
		for _, o := range results {
			if payload, err := util.ParseJSONOutput(string(o.Stdout)); err == nil && o.Succeeded() {
				if rows, err := sessionsFromJSON(ctx, payload); err == nil && len(rows) == 0 {
					good++
				}
			}
			se.Write(o.Stderr)
			se.WriteByte('\n')
		}
		final := ctx.RunAction("list", nil, nil, nil, 3*time.Second)
		payload, parseErr := util.ParseJSONOutput(string(final.Stdout))
		rows, rowsErr := sessionsFromJSON(ctx, payload)
		sockets, socketErr := runtimeSockets(ctx.Runtime)
		ok := good == 8 && final.Succeeded() && parseErr == nil && rowsErr == nil && len(rows) == 0 && socketErr == nil && sockets > 0
		return ok, fmt.Sprintf("successful concurrent JSON clients=%d/8; final exit=%d; runtime sockets=%d (single owner still requires review)", good, final.ReturnCode, sockets), final.Stdout, se.Bytes()
	})}
}

// ---------- stage 32 ----------

func tailOneIsBounded(full, one []byte) bool {
	return completeLog(full) && bytes.Equal(bytes.ReplaceAll(one, []byte("\r\n"), []byte("\n")), []byte("LOG20\n"))
}

func completeLog(output []byte) bool {
	var expected strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&expected, "LOG%02d\n", i)
	}
	return bytes.Equal(bytes.ReplaceAll(output, []byte("\r\n"), []byte("\n")), []byte(expected.String()))
}

func stage32(ctx *runner.Context) []model.CheckResult {
	var out []model.CheckResult
	out = append(out, single(ctx, "logs returns recent output without attaching", func() (bool, string, []byte, []byte) {
		name := fmt.Sprintf("verify-%d-logs", os.Getpid())
		marker := "__DMUX_LOG_" + nonce() + "__"
		pidfile := filepath.Join(ctx.TempDir, "logs-live.pid")
		created := ctx.NewSession(name, []string{"sh", "-c", fmt.Sprintf("echo $$ > %s; printf '%%s' %s; sleep 60", shellQuote(pidfile), shellQuote(marker))}, ctx.Timeout)
		if created.ReturnCode != 0 {
			return false, "failed to create logging session", created.Stdout, created.Stderr
		}
		if _, alive := livePID(pidfile, ctx.Timeout); !alive {
			return false, "logging session command must be live", created.Stdout, created.Stderr
		}
		var r runner.CommandOutput
		ok := util.WaitUntil(func() bool {
			r = ctx.RunAction("logs", map[string]string{"name": name, "tail": "100"}, nil, nil, 0)
			return r.Succeeded() && bytes.Equal(r.Stdout, []byte(marker))
		}, ctx.Timeout)
		return ok, fmt.Sprintf("exit=%d; complete output without trailing newline=%v", r.ReturnCode, ok), r.Stdout, r.Stderr
	}))
	out = append(out, single(ctx, "logs honors --tail bound and queries exited sessions", func() (bool, string, []byte, []byte) {
		name := fmt.Sprintf("verify-%d-logstail", os.Getpid())
		pidfile := filepath.Join(ctx.TempDir, "logs-tail.pid")
		release := filepath.Join(ctx.TempDir, "logs-release")
		script := fmt.Sprintf(`echo $$ > %s; i=1; while [ "$i" -le 20 ]; do printf 'LOG%%02d\n' "$i"; i=$((i+1)); done; while [ ! -f %s ]; do sleep 0.05; done`, shellQuote(pidfile), shellQuote(release))
		created := ctx.NewSession(name, []string{"sh", "-c", script}, 3*time.Second)
		if created.ReturnCode != 0 {
			return false, "failed to create logging session", created.Stdout, created.Stderr
		}
		pid, alive := livePID(pidfile, ctx.Timeout)
		if !alive {
			return false, "logging session must be live before testing live history", created.Stdout, created.Stderr
		}
		var full runner.CommandOutput
		ready := util.WaitUntil(func() bool {
			full = ctx.RunAction("logs", map[string]string{"name": name, "tail": "100"}, nil, nil, 0)
			return full.Succeeded() && completeLog(full.Stdout)
		}, ctx.Timeout)
		if !ready {
			return false, "tail 100 must return all 20 lines in order, exactly once", full.Stdout, full.Stderr
		}
		one := ctx.RunAction("logs", map[string]string{"name": name, "tail": "1"}, nil, nil, 0)
		if full.ReturnCode != 0 || one.ReturnCode != 0 {
			so := append(append([]byte{}, full.Stdout...), one.Stdout...)
			se := append(append([]byte{}, full.Stderr...), one.Stderr...)
			return false, fmt.Sprintf("tail100 exit=%d; tail1 exit=%d", full.ReturnCode, one.ReturnCode), so, se
		}
		hasLast := bytes.Contains(full.Stdout, []byte("LOG20")) && bytes.Contains(one.Stdout, []byte("LOG20"))
		bounded := tailOneIsBounded(full.Stdout, one.Stdout)
		if err := os.WriteFile(release, []byte("exit"), 0o600); err != nil {
			return false, err.Error(), nil, nil
		}
		if !util.WaitUntil(func() bool { return !util.ProcessAlive(pid) }, ctx.Timeout) {
			return false, "session command did not exit after release", nil, nil
		}
		record, _, _, rc := pollInspect(ctx, name, ctx.Timeout)
		if rc != 0 || !normalExit(ctx, record, 0) {
			return false, "inspect must confirm session exit before querying exited history", nil, nil
		}
		after := ctx.RunAction("logs", map[string]string{"name": name, "tail": "100"}, nil, nil, 0)
		exitedOK := after.Succeeded() && completeLog(after.Stdout)
		ok := hasLast && bounded && exitedOK
		detail := fmt.Sprintf("tail100=%dB tail1=%dB last-present=%v exited-record=%v", len(full.Stdout), len(one.Stdout), hasLast, exitedOK)
		so := append(append([]byte{}, full.Stdout...), after.Stdout...)
		return ok, detail, so, one.Stderr
	}))
	return out
}

// ---------- stage 33 ----------

func stage33(ctx *runner.Context) []model.CheckResult {
	var out []model.CheckResult
	out = append(out, single(ctx, "runtime paths are private to the owner", func() (bool, string, []byte, []byte) {
		name := fmt.Sprintf("verify-%d-perm", os.Getpid())
		created := ctx.NewSession(name, []string{"sleep", "60"}, 3*time.Second)
		if created.ReturnCode != 0 {
			return false, "failed to create session", created.Stdout, created.Stderr
		}
		var bad []string
		walkErr := filepath.Walk(ctx.Runtime, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			mode := info.Mode().Perm()
			if mode&0o077 != 0 {
				rel, _ := filepath.Rel(ctx.Runtime, p)
				bad = append(bad, fmt.Sprintf("%s:%04o", rel, mode))
			}
			return nil
		})
		if walkErr != nil {
			return false, walkErr.Error(), nil, nil
		}
		rootInfo, err := os.Stat(ctx.Runtime)
		if err != nil {
			return false, err.Error(), nil, nil
		}
		rootOK := rootInfo.Mode().Perm() == 0o700
		sockets, socketErr := runtimeSockets(ctx.Runtime)
		return rootOK && len(bad) == 0 && socketErr == nil && sockets > 0, fmt.Sprintf("runtime root=%04o; group/world-accessible paths=%v; session/control sockets=%d", rootInfo.Mode().Perm(), bad, sockets), nil, nil
	}))
	out = append(out, single(ctx, "a symlinked runtime path is rejected or safely repaired", func() (bool, string, []byte, []byte) {
		probe, err := runner.New(ctx.Project, ctx.Config)
		if err != nil {
			return false, err.Error(), nil, nil
		}
		target := probe.Environment("DMUX_RUNTIME_DIR")
		if target == "" || filepath.Clean(target) == filepath.Clean(probe.TempDir) {
			probe.Close()
			return false, "DMUX_RUNTIME_DIR must name an isolated runtime path", nil, nil
		}
		rel, err := filepath.Rel(probe.TempDir, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			probe.Close()
			return false, fmt.Sprintf("configured runtime path escapes test runtime: %s", target), nil, nil
		}
		entries, err := os.ReadDir(target)
		if err != nil || len(entries) != 0 {
			probe.Close()
			return false, fmt.Sprintf("fresh runtime path is not empty: %v", err), nil, nil
		}
		if err := os.Remove(target); err != nil {
			probe.Close()
			return false, err.Error(), nil, nil
		}
		outside, err := os.MkdirTemp("", "dmux-outside-")
		if err != nil {
			probe.Close()
			return false, err.Error(), nil, nil
		}
		defer func() {
			probe.Close()
			_ = os.RemoveAll(outside)
		}()
		if err := os.WriteFile(filepath.Join(outside, "sentinel"), []byte("unchanged"), 0o600); err != nil {
			return false, err.Error(), nil, nil
		}
		beforeSentinel, err := os.Lstat(filepath.Join(outside, "sentinel"))
		if err != nil {
			return false, err.Error(), nil, nil
		}
		beforeOutside, err := os.Stat(outside)
		if err != nil {
			return false, err.Error(), nil, nil
		}
		if err := os.Symlink(outside, target); err != nil {
			return false, err.Error(), nil, nil
		}
		// Point both supported variables at the compromised path so ignoring
		// one variable cannot make this probe pass accidentally.
		probe.Env = replaceEnv(probe.Env, "XDG_RUNTIME_DIR", target)
		probe.Env = replaceEnv(probe.Env, "DMUX_RUNTIME_DIR", target)
		name := fmt.Sprintf("verify-%d-symlink", os.Getpid())
		created := probe.NewSession(name, []string{"sleep", "60"}, 3*time.Second)
		outsideEntries, readErr := os.ReadDir(outside)
		outsideChanged := readErr != nil || len(outsideEntries) != 1 || outsideEntries[0].Name() != "sentinel"
		content, contentErr := os.ReadFile(filepath.Join(outside, "sentinel"))
		afterSentinel, sentinelErr := os.Lstat(filepath.Join(outside, "sentinel"))
		afterOutside, outsideErr := os.Stat(outside)
		outsideChanged = outsideChanged || contentErr != nil || !bytes.Equal(content, []byte("unchanged")) || sentinelErr != nil || outsideErr != nil
		if sentinelErr == nil && outsideErr == nil {
			outsideChanged = outsideChanged || !os.SameFile(beforeSentinel, afterSentinel) || beforeSentinel.Mode() != afterSentinel.Mode() || !beforeSentinel.ModTime().Equal(afterSentinel.ModTime()) || !os.SameFile(beforeOutside, afterOutside) || beforeOutside.Mode() != afterOutside.Mode() || !beforeOutside.ModTime().Equal(afterOutside.ModTime())
		}
		fi, statErr := os.Lstat(target)
		repaired := statErr == nil && fi.Mode()&os.ModeSymlink == 0 && fi.IsDir() && fi.Mode().Perm()&0o077 == 0
		rejected := created.Failed()
		ok := !outsideChanged && (rejected || (created.Succeeded() && repaired))
		return ok, fmt.Sprintf("new exit=%d; outside modified=%v; repaired=%v", created.ReturnCode, outsideChanged, repaired), created.Stdout, created.Stderr
	}))
	out = append(out, single(ctx, "an insecure pre-existing runtime is rejected or repaired", func() (bool, string, []byte, []byte) {
		probe, err := runner.New(ctx.Project, ctx.Config)
		if err != nil {
			return false, err.Error(), nil, nil
		}
		defer probe.Close()
		target := probe.Environment("DMUX_RUNTIME_DIR")
		if target == "" {
			return false, "DMUX_RUNTIME_DIR is not configured", nil, nil
		}
		if err := os.Chmod(target, 0o755); err != nil {
			return false, err.Error(), nil, nil
		}
		defer os.Chmod(target, 0o700)
		probe.Env = replaceEnv(probe.Env, "XDG_RUNTIME_DIR", target)
		probe.Env = replaceEnv(probe.Env, "DMUX_RUNTIME_DIR", target)
		r := probe.RunAction("list", nil, nil, nil, 3*time.Second)
		fi, statErr := os.Stat(target)
		repaired := statErr == nil && fi.IsDir() && fi.Mode().Perm()&0o077 == 0
		ok := r.Failed() || (r.Succeeded() && repaired)
		return ok, fmt.Sprintf("list exit=%d; repaired=%v", r.ReturnCode, repaired), r.Stdout, r.Stderr
	}))
	return out
}

func replaceEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := append([]string(nil), env...)
	for i, item := range out {
		if strings.HasPrefix(item, prefix) {
			out[i] = prefix + value
			return out
		}
	}
	return append(out, prefix+value)
}

// ---------- stage 37 ----------

func stage37(ctx *runner.Context) []model.CheckResult {
	var results []model.CheckResult
	results = append(results, single(ctx, "Go test suite passes", func() (bool, string, []byte, []byte) {
		if _, err := os.Stat(filepath.Join(ctx.Workdir, "go.mod")); err != nil {
			return false, "go.mod not found in configured working_directory", nil, nil
		}
		r := ctx.Run([]string{"go", "test", "./..."}, nil, 120*time.Second, "")
		return r.ReturnCode == 0, fmt.Sprintf("exit=%d", r.ReturnCode), r.Stdout, r.Stderr
	}))
	results = append(results, single(ctx, "Go race detector passes", func() (bool, string, []byte, []byte) {
		if _, err := os.Stat(filepath.Join(ctx.Workdir, "go.mod")); err != nil {
			return false, "go.mod not found in configured working_directory", nil, nil
		}
		r := ctx.Run([]string{"go", "test", "-race", "./..."}, nil, 180*time.Second, "")
		return r.ReturnCode == 0, fmt.Sprintf("exit=%d", r.ReturnCode), r.Stdout, r.Stderr
	}))
	return results
}

// ---------- registry ----------

// Registry maps built-in check group names to implementations.
var Registry = map[string]CheckFn{
	"stage_1": stage1, "stage_2": stage2, "stage_3": stage3,
	"stage_5": stage5, "stage_6": stage6, "stage_7": stage7,
	"stage_8": stage8, "stage_9": stage9, "stage_10": stage10,
	"stage_11": stage11, "stage_12": stage12, "stage_13": stage13,
	"stage_14": stage14, "stage_15": stage15, "stage_16": stage16,
	"stage_17": stage17, "stage_18": stage18, "stage_20": stage20,
	"stage_32": stage32, "stage_33": stage33, "stage_37": stage37,
}

// RunBuiltin runs one named group.
func RunBuiltin(name string, ctx *runner.Context) ([]model.CheckResult, error) {
	fn, ok := Registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown built-in check group: %s", name)
	}
	return fn(ctx), nil
}

// RunCustomChecks executes config custom_checks[stage] without a shell.
func RunCustomChecks(stage int, ctx *runner.Context) []model.CheckResult {
	var results []model.CheckResult
	cc, _ := ctx.Config["custom_checks"].(map[string]any)
	items, _ := cc[itoa(stage)].([]any)
	for i, item := range items {
		var name string
		var command []string
		timeout := 120 * time.Second
		if arr, ok := item.([]any); ok {
			name = fmt.Sprintf("custom check %d", i+1)
			for _, e := range arr {
				command = append(command, fmt.Sprintf("%v", e))
			}
		} else if m, ok := item.(map[string]any); ok {
			name, _ = m["name"].(string)
			if name == "" {
				name = fmt.Sprintf("custom check %d", i+1)
			}
			if arr, ok := m["command"].([]any); ok {
				for _, e := range arr {
					command = append(command, fmt.Sprintf("%v", e))
				}
			}
			if f, ok := numVal(m["timeout_seconds"]); ok {
				timeout = time.Duration(f * float64(time.Second))
			}
		} else {
			name = fmt.Sprintf("custom check %d", i+1)
		}
		start := time.Now()
		if len(command) == 0 || command[0] == "" {
			results = append(results, ctx.Result(name, false, "custom check command must contain an executable", start, nil, nil))
			continue
		}
		r := ctx.Run(command, nil, timeout, "")
		results = append(results, ctx.Result(name, r.ReturnCode == 0, fmt.Sprintf("exit=%d; command=%s", r.ReturnCode, strings.Join(command, " ")), start, r.Stdout, r.Stderr))
	}
	return results
}

func numVal(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	}
	return 0, false
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
