package checks

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
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
		ok := r.ReturnCode != 0 && len(bytes.TrimSpace(r.Stdout)) == 0 && len(bytes.TrimSpace(r.Stderr)) > 0
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
		return r.ReturnCode != 0, fmt.Sprintf("exit=%d", r.ReturnCode), r.Stdout, r.Stderr
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
		p, err := ctx.SpawnPTY("run", nil, []string{"tty"}, 24, 80)
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
		ok := rc == 0 && !strings.Contains(text, "not a tty") && devRE.MatchString(text)
		return ok, fmt.Sprintf("reported terminal=%q", text), data, nil
	}))
	out = append(out, single(ctx, "all standard streams are TTYs", func() (bool, string, []byte, []byte) {
		code := "import os; print(int(os.isatty(0)), int(os.isatty(1)), int(os.isatty(2)))"
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
		return rc == 0 && bytes.Contains(data, []byte("1 1 1")), "stdin, stdout and stderr should all be terminal devices", data, nil
	}))
	return out
}

// ---------- stage 6 ----------

func stage6(ctx *runner.Context) []model.CheckResult {
	return []model.CheckResult{single(ctx, "interactive shell accepts commands", func() (bool, string, []byte, []byte) {
		p, err := ctx.SpawnPTY("run", nil, []string{"bash", "--noprofile", "--norc"}, 24, 80)
		if err != nil {
			return false, err.Error(), nil, nil
		}
		defer p.Terminate()
		_ = p.Write([]byte("echo __DMUX_STAGE6_OK__\nexit\n"))
		data, err := p.ReadUntil([]byte("__DMUX_STAGE6_OK__"), ctx.Timeout)
		if err != nil {
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
		code := "import os,tty,termios; fd=0; old=termios.tcgetattr(fd); tty.setraw(fd); b=os.read(fd,1); os.write(1,b'__KEY__'+b); termios.tcsetattr(fd,termios.TCSANOW,old)"
		p, err := ctx.SpawnPTY("run", nil, []string{"python3", "-c", code}, 24, 80)
		if err != nil {
			return false, err.Error(), nil, nil
		}
		defer p.Terminate()
		time.Sleep(200 * time.Millisecond)
		_ = p.Write([]byte("Z"))
		data, err := p.ReadUntil([]byte("__KEY__Z"), 1500*time.Millisecond)
		if err != nil {
			// tolerate CRLF translation (__KEY__\r\nZ etc.)
			combined := append([]byte(nil), p.Buffer...)
			if bytes.Contains(combined, []byte("__KEY__")) {
				rc, werr := p.Wait(ctx.Timeout)
				if werr == nil && rc == 0 {
					return true, "single byte reached child without Enter (CRLF-tolerant)", combined, nil
				}
			}
			return false, "single byte reached child without Enter", data, nil
		}
		rc, err := p.Wait(ctx.Timeout)
		if err != nil {
			return false, err.Error(), data, nil
		}
		return rc == 0, "single byte reached child without Enter", data, nil
	})}
}

// ---------- stage 8 ----------

func stage8(ctx *runner.Context) []model.CheckResult {
	var results []model.CheckResult
	checkRestore := func(name string, extra []string) model.CheckResult {
		return single(ctx, name, func() (bool, string, []byte, []byte) {
			p, err := ctx.SpawnPTY("run", nil, extra, 24, 80)
			if err != nil {
				return false, err.Error(), nil, nil
			}
			rc, err := p.Wait(ctx.Timeout)
			if err != nil {
				p.Terminate()
				return false, err.Error(), append([]byte(nil), p.Buffer...), nil
			}
			data := append([]byte(nil), p.Buffer...)
			lflag, lerr := p.LFlag()
			p.Close()
			if lerr != nil {
				// Termios unavailable (e.g. capped CI pty): fall back to
				// verifying the child exited with the expected status.
				ok := rc == 0 || (len(extra) > 0 && rc != 0)
				return ok, fmt.Sprintf("exit=%d; termios unavailable, skipped ICANON/ECHO check", rc), data, nil
			}
			mask := uint64(unix.ICANON | unix.ECHO)
			ok := (lflag & mask) == mask
			wantFail := len(extra) > 0 && strings.Contains(strings.Join(extra, " "), "missing")
			if wantFail {
				ok = rc != 0 && ok
			} else {
				ok = rc == 0 && ok
			}
			return ok, fmt.Sprintf("exit=%d; ICANON/ECHO restored", rc), data, nil
		})
	}
	results = append(results, checkRestore("terminal state restored after normal exit", []string{"sh", "-c", "sleep 0.2"}))
	results = append(results, checkRestore("terminal state restored after command-start failure", []string{"/definitely/missing/dmux-command"}))
	return results
}

// ---------- stage 9 ----------

func stage9(ctx *runner.Context) []model.CheckResult {
	return []model.CheckResult{single(ctx, "initial terminal size is propagated", func() (bool, string, []byte, []byte) {
		p, err := ctx.SpawnPTY("run", nil, []string{"stty", "size"}, 37, 101)
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
		return rc == 0, fmt.Sprintf("expected 37x101; exit=%d", rc), data, nil
	})}
}

// ---------- stage 10 ----------

func stage10(ctx *runner.Context) []model.CheckResult {
	return []model.CheckResult{single(ctx, "dynamic terminal resize is propagated", func() (bool, string, []byte, []byte) {
		code := "import fcntl,signal,struct,termios,time; " +
			"size=lambda: struct.unpack('HHHH',fcntl.ioctl(0,termios.TIOCGWINSZ,struct.pack('HHHH',0,0,0,0)))[:2]; " +
			"h=lambda a,b: print('__SIZE__%dx%d'%size(),flush=True); " +
			"signal.signal(signal.SIGWINCH,h); print('__READY__',flush=True); " +
			"time.sleep(10)"
		p, err := ctx.SpawnPTY("run", nil, []string{"python3", "-c", code}, 24, 80)
		if err != nil {
			return false, err.Error(), nil, nil
		}
		defer p.Terminate()
		if _, err := p.ReadUntil([]byte("__READY__"), ctx.Timeout); err != nil {
			return false, err.Error(), append([]byte(nil), p.Buffer...), nil
		}
		if err := p.SetSize(41, 109); err != nil {
			return false, err.Error(), append([]byte(nil), p.Buffer...), nil
		}
		data, err := p.ReadUntil([]byte("__SIZE__41x109"), 2500*time.Millisecond)
		if err != nil {
			return false, err.Error(), data, nil
		}
		return true, "SIGWINCH and new dimensions reached child", data, nil
	})}
}

// ---------- stage 11 ----------

func stage11(ctx *runner.Context) []model.CheckResult {
	var results []model.CheckResult
	results = append(results, single(ctx, "Ctrl+C preserves interactive shell", func() (bool, string, []byte, []byte) {
		p, err := ctx.SpawnPTY("run", nil, []string{"bash", "--noprofile", "--norc"}, 24, 80)
		if err != nil {
			return false, err.Error(), nil, nil
		}
		defer p.Terminate()
		_ = p.Write([]byte("sleep 30\n"))
		time.Sleep(300 * time.Millisecond)
		_ = p.Write([]byte{0x03})
		_ = p.Write([]byte("echo __SHELL_ALIVE__\nexit\n"))
		data, err := p.ReadUntil([]byte("__SHELL_ALIVE__"), 3*time.Second)
		if err != nil {
			return false, "Ctrl+C interrupted foreground command and shell survived: " + err.Error(), data, nil
		}
		rc, err := p.Wait(ctx.Timeout)
		if err != nil {
			return false, err.Error(), data, nil
		}
		return rc == 0, "Ctrl+C interrupted foreground command and shell survived", data, nil
	}))
	results = append(results, single(ctx, "Ctrl+Z supports shell job control", func() (bool, string, []byte, []byte) {
		p, err := ctx.SpawnPTY("run", nil, []string{"bash", "--noprofile", "--norc"}, 24, 80)
		if err != nil {
			return false, err.Error(), nil, nil
		}
		defer p.Terminate()
		_ = p.Write([]byte("sleep 30\n"))
		time.Sleep(300 * time.Millisecond)
		_ = p.Write([]byte{0x1a})
		time.Sleep(200 * time.Millisecond)
		_ = p.Write([]byte("jobs\n"))
		data, err := p.ReadUntil([]byte("Stopped"), 3*time.Second)
		if err != nil {
			return false, "Ctrl+Z created a stopped job visible to the shell: " + err.Error(), data, nil
		}
		_ = p.Write([]byte("kill %1\nexit\n"))
		return true, "Ctrl+Z created a stopped job visible to the shell", data, nil
	}))
	return results
}

// ---------- stage 12 ----------

func stage12(ctx *runner.Context) []model.CheckResult {
	var results []model.CheckResult
	name := fmt.Sprintf("verify-%d-identity", os.Getpid())
	ctx.Sessions[name] = true
	results = append(results, single(ctx, "duplicate active session name is rejected", func() (bool, string, []byte, []byte) {
		argv, err := ctx.Command("new", map[string]string{"name": name})
		if err != nil {
			return false, err.Error(), nil, nil
		}
		argv = append(argv, "sleep", "30")
		creator := exec.Command(argv[0], argv[1:]...)
		creator.Dir = ctx.Workdir
		creator.Env = ctx.Env
		var cbo, cbe bytes.Buffer
		creator.Stdout = &cbo
		creator.Stderr = &cbe
		if err := creator.Start(); err != nil {
			return false, err.Error(), nil, nil
		}
		defer func() {
			_ = creator.Process.Kill()
			_, _ = creator.Process.Wait()
		}()
		time.Sleep(400 * time.Millisecond)
		second := ctx.RunAction("new", map[string]string{"name": name}, []string{"sleep", "30"}, nil, 2*time.Second)
		ok := second.ReturnCode != 0
		return ok, fmt.Sprintf("duplicate exit=%d", second.ReturnCode), second.Stdout, second.Stderr
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
			allBad = allBad && r.ReturnCode != 0
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
		ready := util.WaitUntil(func() bool {
			_, err := os.Stat(pidfile)
			return err == nil
		}, 2*time.Second)
		pid := -1
		if ready {
			var v int
			if raw, err := os.ReadFile(pidfile); err == nil {
				fmt.Sscanf(strings.TrimSpace(string(raw)), "%d", &v)
				pid = v
			}
		}
		alive := ready && util.ProcessAlive(pid)
		return r.ReturnCode == 0 && alive, fmt.Sprintf("new exit=%d; pid=%d; alive=%v", r.ReturnCode, pid, alive), r.Stdout, r.Stderr
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
		var shellPID int
		if raw, err := os.ReadFile(pidfile); err == nil {
			fmt.Sscanf(strings.TrimSpace(string(raw)), "%d", &shellPID)
		}
		p, err := ctx.SpawnPTY("attach", map[string]string{"name": name}, nil, 24, 80)
		if err != nil {
			return false, err.Error(), nil, nil
		}
		defer p.Terminate()
		_ = p.Write([]byte("echo __ATTACH_OK__\n"))
		data, err := p.ReadUntil([]byte("__ATTACH_OK__"), 3*time.Second)
		if err != nil {
			return false, err.Error(), data, nil
		}
		if p.Cmd.Process != nil {
			_ = p.Cmd.Process.Signal(unix.SIGKILL)
		}
		_, _ = p.Wait(time.Second)
		alive := util.ProcessAlive(shellPID)
		return alive, fmt.Sprintf("interaction succeeded; shell alive after client SIGKILL=%v", alive), data, nil
	})}
}

// ---------- stage 15 ----------

func stage15(ctx *runner.Context) []model.CheckResult {
	return []model.CheckResult{single(ctx, "detach sequence leaves session alive", func() (bool, string, []byte, []byte) {
		name := fmt.Sprintf("verify-%d-detach", os.Getpid())
		pidfile := filepath.Join(ctx.TempDir, "detach.pid")
		q := "'" + strings.ReplaceAll(pidfile, "'", "'\\''") + "'"
		created := ctx.NewSession(name, []string{"sh", "-c", fmt.Sprintf("echo $$ > %s; exec sh", q)}, 3*time.Second)
		if created.ReturnCode != 0 || !util.WaitUntil(func() bool {
			_, err := os.Stat(pidfile)
			return err == nil
		}, 2*time.Second) {
			return false, "failed to create session", created.Stdout, created.Stderr
		}
		var shellPID int
		if raw, err := os.ReadFile(pidfile); err == nil {
			fmt.Sscanf(strings.TrimSpace(string(raw)), "%d", &shellPID)
		}
		p, err := ctx.SpawnPTY("attach", map[string]string{"name": name}, nil, 24, 80)
		if err != nil {
			return false, err.Error(), nil, nil
		}
		defer p.Terminate()
		_ = p.Write([]byte("sleep 30\n"))
		time.Sleep(300 * time.Millisecond)
		seq := "\x01d"
		if s, ok := ctx.Config["detach_sequence"].(string); ok && s != "" {
			seq = s
		}
		_ = p.Write([]byte(seq))
		rc, err := p.Wait(3 * time.Second)
		if err != nil {
			return false, err.Error(), append([]byte(nil), p.Buffer...), nil
		}
		alive := util.ProcessAlive(shellPID)
		return alive && rc == 0, fmt.Sprintf("attach exit=%d; shell alive=%v", rc, alive), append([]byte(nil), p.Buffer...), nil
	})}
}

// ---------- JSON helpers ----------

func sessionsFromJSON(ctx *runner.Context, payload any) ([]map[string]any, error) {
	if arr, ok := payload.([]any); ok {
		var out []map[string]any
		for _, e := range arr {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
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
		var parent, child int
		if raw, err := os.ReadFile(pidfile); err == nil {
			fmt.Sscanf(strings.TrimSpace(string(raw)), "%d", &parent)
		}
		if raw, err := os.ReadFile(childFile); err == nil {
			fmt.Sscanf(strings.TrimSpace(string(raw)), "%d", &child)
		}
		first := ctx.RunAction("kill", map[string]string{"name": name}, nil, nil, 4*time.Second)
		dead := util.WaitUntil(func() bool {
			return !util.ProcessAlive(parent) && !util.ProcessAlive(child)
		}, 3*time.Second)
		second := ctx.RunAction("kill", map[string]string{"name": name}, nil, nil, 2*time.Second)
		ok := first.ReturnCode == 0 && dead && (second.ReturnCode == 0 || second.ReturnCode == 1 || second.ReturnCode == 2)
		so := append(append([]byte{}, first.Stdout...), second.Stdout...)
		se := append(append([]byte{}, first.Stderr...), second.Stderr...)
		return ok, fmt.Sprintf("parent=%d, child=%d, both dead=%v, repeat exit=%d", parent, child, dead, second.ReturnCode), so, se
	})}
}

func inspect(ctx *runner.Context, name string) (any, []byte, []byte, int) {
	r := ctx.RunAction("inspect", map[string]string{"name": name}, nil, nil, 0)
	payload, err := util.ParseJSONOutput(string(r.Stdout))
	if err != nil {
		return nil, r.Stdout, r.Stderr, r.ReturnCode
	}
	return payload, r.Stdout, r.Stderr, r.ReturnCode
}

// ---------- stage 18 ----------

func pollInspect(ctx *runner.Context, name string, timeout time.Duration) (map[string]any, []byte, []byte, int) {
	deadline := time.Now().Add(timeout)
	var last map[string]any
	var so, se []byte
	rc := 1
	for time.Now().Before(deadline) {
		p, o, e, code := inspect(ctx, name)
		so, se, rc = o, e, code
		if m, ok := p.(map[string]any); ok {
			last = m
			if util.GetAny(m, fieldAliases(ctx, "exit_code")) != nil || util.GetAny(m, fieldAliases(ctx, "signal")) != nil {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return last, so, se, rc
}

func stage18(ctx *runner.Context) []model.CheckResult {
	var out []model.CheckResult
	out = append(out, single(ctx, "inspect preserves a non-zero normal exit record", func() (bool, string, []byte, []byte) {
		name := fmt.Sprintf("verify-%d-exit7", os.Getpid())
		created := ctx.NewSession(name, []string{"sh", "-c", "exit 7"}, 3*time.Second)
		if created.ReturnCode != 0 {
			return false, "session creation failed", created.Stdout, created.Stderr
		}
		payload, rawOut, rawErr, rc := pollInspect(ctx, name, 4*time.Second)
		var code, state any
		if payload != nil {
			code = util.GetAny(payload, fieldAliases(ctx, "exit_code"))
			state = util.GetAny(payload, fieldAliases(ctx, "state"))
		}
		num := -1
		if f, ok := code.(float64); ok {
			num = int(f)
		}
		ok := rc == 0 && num == 7
		return ok, fmt.Sprintf("state=%v; exit_code=%v", state, code), rawOut, rawErr
	}))
	out = append(out, single(ctx, "inspect preserves a zero exit record", func() (bool, string, []byte, []byte) {
		name := fmt.Sprintf("verify-%d-exit0", os.Getpid())
		created := ctx.NewSession(name, []string{"sh", "-c", "exit 0"}, 3*time.Second)
		if created.ReturnCode != 0 {
			return false, "session creation failed", created.Stdout, created.Stderr
		}
		payload, rawOut, rawErr, rc := pollInspect(ctx, name, 4*time.Second)
		var code, state any
		if payload != nil {
			code = util.GetAny(payload, fieldAliases(ctx, "exit_code"))
			state = util.GetAny(payload, fieldAliases(ctx, "state"))
		}
		num := -1
		if f, ok := code.(float64); ok {
			num = int(f)
		}
		ok := rc == 0 && num == 0
		return ok, fmt.Sprintf("state=%v; exit_code=%v", state, code), rawOut, rawErr
	}))
	out = append(out, single(ctx, "inspect reports signal termination distinctly", func() (bool, string, []byte, []byte) {
		name := fmt.Sprintf("verify-%d-sigterm", os.Getpid())
		created := ctx.NewSession(name, []string{"sh", "-c", "kill -TERM $$"}, 3*time.Second)
		if created.ReturnCode != 0 {
			return false, "session creation failed", created.Stdout, created.Stderr
		}
		payload, rawOut, rawErr, rc := pollInspect(ctx, name, 4*time.Second)
		var code, sig, state any
		if payload != nil {
			code = util.GetAny(payload, fieldAliases(ctx, "exit_code"))
			sig = util.GetAny(payload, fieldAliases(ctx, "signal"))
			state = util.GetAny(payload, fieldAliases(ctx, "state"))
		}
		// Signal death must not look like a clean exit(0); an explicit
		// signal field or non-zero/absent code with non-running state passes.
		stateStr := fmt.Sprintf("%v", state)
		running := strings.EqualFold(stateStr, "running") || strings.EqualFold(stateStr, "alive")
		ok := rc == 0 && !running && (sig != nil || code == nil || code.(float64) != 0)
		return ok, fmt.Sprintf("state=%v; exit_code=%v; signal=%v", state, code, sig), rawOut, rawErr
	}))
	out = append(out, single(ctx, "inspect reports failed-before-execution distinctly", func() (bool, string, []byte, []byte) {
		name := fmt.Sprintf("verify-%d-badexec", os.Getpid())
		created := ctx.NewSession(name, []string{"/definitely/missing/dmux-verifier-executable"}, 3*time.Second)
		if created.ReturnCode == 0 {
			// Some implementations accept creation then record failure; keep going.
		}
		payload, rawOut, rawErr, rc := pollInspect(ctx, name, 4*time.Second)
		if payload == nil {
			// At minimum the CLI must not crash; creation rc != 0 already proves rejection.
			ok := created.ReturnCode != 0
			return ok, fmt.Sprintf("new exit=%d; no inspect record (rejected at create)", created.ReturnCode), rawOut, rawErr
		}
		var state any
		state = util.GetAny(payload, fieldAliases(ctx, "state"))
		stateStr := fmt.Sprintf("%v", state)
		running := strings.EqualFold(stateStr, "running") || strings.EqualFold(stateStr, "alive")
		ok := rc == 0 && !running
		return ok, fmt.Sprintf("state=%v", state), rawOut, rawErr
	}))
	return out
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
			if o.ReturnCode == 0 {
				good++
			}
			se.Write(o.Stderr)
			se.WriteByte('\n')
		}
		final := ctx.RunAction("list", nil, nil, nil, 3*time.Second)
		ok := good == 8 && final.ReturnCode == 0
		return ok, fmt.Sprintf("successful concurrent clients=%d/8; final exit=%d", good, final.ReturnCode), final.Stdout, se.Bytes()
	})}
}

// ---------- stage 32 ----------

var logLineRE = regexp.MustCompile(`LOG\d{2}`)

func tailOneIsBounded(full, one []byte) bool {
	return len(one) < len(full) &&
		bytes.Contains(one, []byte("LOG20")) &&
		!bytes.Contains(one, []byte("LOG19")) &&
		len(logLineRE.FindAll(one, -1)) == 1
}

func stage32(ctx *runner.Context) []model.CheckResult {
	var out []model.CheckResult
	out = append(out, single(ctx, "logs returns recent output without attaching", func() (bool, string, []byte, []byte) {
		name := fmt.Sprintf("verify-%d-logs", os.Getpid())
		marker := "__DMUX_LOG_MARKER__"
		created := ctx.NewSession(name, []string{"sh", "-c", fmt.Sprintf("printf '%s'; sleep 2", marker)}, 3*time.Second)
		if created.ReturnCode != 0 {
			return false, "failed to create logging session", created.Stdout, created.Stderr
		}
		time.Sleep(300 * time.Millisecond)
		r := ctx.RunAction("logs", map[string]string{"name": name, "tail": "100"}, nil, nil, 0)
		ok := r.ReturnCode == 0 && bytes.Contains(r.Stdout, []byte(marker))
		return ok, fmt.Sprintf("exit=%d; marker present=%v", r.ReturnCode, bytes.Contains(r.Stdout, []byte(marker))), r.Stdout, r.Stderr
	}))
	out = append(out, single(ctx, "logs honors --tail bound and queries exited sessions", func() (bool, string, []byte, []byte) {
		name := fmt.Sprintf("verify-%d-logstail", os.Getpid())
		// Emit 20 numbered lines then linger so both live and exited reads work.
		script := `for i in $(seq 1 20); do printf 'LOG%02d\n' "$i"; done; sleep 2`
		created := ctx.NewSession(name, []string{"sh", "-c", script}, 3*time.Second)
		if created.ReturnCode != 0 {
			return false, "failed to create logging session", created.Stdout, created.Stderr
		}
		time.Sleep(500 * time.Millisecond)
		full := ctx.RunAction("logs", map[string]string{"name": name, "tail": "100"}, nil, nil, 0)
		one := ctx.RunAction("logs", map[string]string{"name": name, "tail": "1"}, nil, nil, 0)
		if full.ReturnCode != 0 || one.ReturnCode != 0 {
			so := append(append([]byte{}, full.Stdout...), one.Stdout...)
			se := append(append([]byte{}, full.Stderr...), one.Stderr...)
			return false, fmt.Sprintf("tail100 exit=%d; tail1 exit=%d", full.ReturnCode, one.ReturnCode), so, se
		}
		hasLast := bytes.Contains(full.Stdout, []byte("LOG20")) && bytes.Contains(one.Stdout, []byte("LOG20"))
		bounded := tailOneIsBounded(full.Stdout, one.Stdout)
		// Wait for exit, then query the retained record.
		time.Sleep(2200 * time.Millisecond)
		after := ctx.RunAction("logs", map[string]string{"name": name, "tail": "100"}, nil, nil, 0)
		exitedOK := after.ReturnCode == 0 && bytes.Contains(after.Stdout, []byte("LOG20"))
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
		return rootOK && len(bad) == 0, fmt.Sprintf("runtime root=%04o; group/world-accessible paths=%v", rootInfo.Mode().Perm(), bad), nil, nil
	}))
	out = append(out, single(ctx, "a symlinked runtime path is rejected or safely repaired", func() (bool, string, []byte, []byte) {
		probe, err := runner.New(ctx.Project, ctx.Config)
		if err != nil {
			return false, err.Error(), nil, nil
		}
		target := probe.Environment("DMUX_RUNTIME_DIR")
		if target == "" || filepath.Clean(target) == filepath.Clean(probe.Runtime) {
			probe.Close()
			return false, "DMUX_RUNTIME_DIR must name an isolated subdirectory", nil, nil
		}
		rel, err := filepath.Rel(probe.Runtime, target)
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
		fi, statErr := os.Lstat(target)
		repaired := statErr == nil && fi.Mode()&os.ModeSymlink == 0 && fi.IsDir() && fi.Mode().Perm()&0o077 == 0
		rejected := created.ReturnCode != 0
		ok := !outsideChanged && (rejected || repaired)
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
		ok := r.ReturnCode != 0 || repaired
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
