package checks

import (
	"crypto/rand"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Alisjj/durablemux-verifier/internal/ptyproc"
	"github.com/Alisjj/durablemux-verifier/internal/runner"
	"github.com/Alisjj/durablemux-verifier/internal/util"
)

var childTTYRE = regexp.MustCompile(`__DMUX_TTY__(/dev/[^\s]+)`)

func separateTTY(p *ptyproc.Process, output []byte) bool {
	m := childTTYRE.FindSubmatch(output)
	return len(m) == 2 && string(m[1]) != p.OuterTTY
}

func nonce() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", b)
}

// The expected response is absent from the submitted input, so terminal echo
// cannot be mistaken for execution by the session shell.
func shellChallenge(p *ptyproc.Process, timeout time.Duration) ([]byte, error) {
	id := nonce()
	if err := p.Write([]byte("printf '__DMUX_RESPONSE_%s__\\n' '" + id + "'\n")); err != nil {
		return append([]byte(nil), p.Buffer...), err
	}
	return p.ReadUntil([]byte("__DMUX_RESPONSE_"+id+"__"), timeout)
}

func readPID(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 1 {
		return 0, fmt.Errorf("invalid PID in %s: %q", path, raw)
	}
	return pid, nil
}

func spawnShell(ctx *runner.Context) (*ptyproc.Process, error) {
	argv, err := ctx.Command("run", nil)
	if err != nil {
		return nil, err
	}
	argv = append(argv, "bash", "--noprofile", "--norc", "-i")
	p, err := ptyproc.Start(argv, ctx.Workdir, replaceEnv(ctx.Env, "PS1", "__DMUX_PROMPT__ "), 24, 80)
	if err != nil {
		return nil, err
	}
	if _, err := p.ReadUntil([]byte("__DMUX_PROMPT__ "), ctx.Timeout); err != nil {
		p.Terminate()
		return nil, fmt.Errorf("interactive shell did not become ready: %w", err)
	}
	return p, nil
}

type foregroundJob struct{ stateFile string }

func startForegroundJob(ctx *runner.Context, p *ptyproc.Process) (*foregroundJob, error) {
	pidfile := filepath.Join(ctx.TempDir, "foreground-"+nonce()+".pid")
	job := &foregroundJob{stateFile: pidfile + ".foreground"}
	code := fmt.Sprintf(`import os,time
open(%q,'w').write(str(os.getpid()))
print('__JOB_'+'READY__',flush=True)
end=time.monotonic()+60
while time.monotonic()<end:
 open(%q,'w').write('1' if os.tcgetpgrp(0)==os.getpgrp() else '0')
 time.sleep(0.02)
`, pidfile, job.stateFile)
	if err := p.Write([]byte("python3 -c " + shellQuote(code) + "\n")); err != nil {
		return nil, err
	}
	if _, err := p.ReadUntil([]byte("__JOB_READY__"), ctx.Timeout); err != nil {
		return nil, err
	}
	if _, err := readPID(pidfile); err != nil {
		return nil, err
	}
	if !waitForeground(job, ctx.Timeout, true) {
		return nil, fmt.Errorf("job must own the child terminal foreground process group")
	}
	return job, nil
}

func waitForeground(job *foregroundJob, timeout time.Duration, foreground bool) bool {
	expected := "0"
	if foreground {
		expected = "1"
	}
	return util.WaitUntil(func() bool {
		state, err := os.ReadFile(job.stateFile)
		return err == nil && string(state) == expected
	}, timeout)
}

func livePID(path string, timeout time.Duration) (int, bool) {
	pid := 0
	ready := util.WaitUntil(func() bool {
		var err error
		pid, err = readPID(path)
		return err == nil && util.ProcessAlive(pid)
	}, timeout)
	return pid, ready
}

func integer(v any) (int, bool) {
	n, ok := v.(float64)
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || n < 0 || n > 1<<31-1 {
		return 0, false
	}
	return int(n), true
}

func stateIs(v any, states ...string) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	for _, state := range states {
		if strings.EqualFold(s, state) {
			return true
		}
	}
	return false
}
