package checks

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Alisjj/durablemux-verifier/internal/model"
	"github.com/Alisjj/durablemux-verifier/internal/runner"
	"github.com/Alisjj/durablemux-verifier/internal/util"
)

func inspect(ctx *runner.Context, name string) (any, []byte, []byte, int) {
	r := ctx.RunAction("inspect", map[string]string{"name": name}, nil, nil, 0)
	payload, err := util.ParseJSONOutput(string(r.Stdout))
	if err != nil {
		return nil, r.Stdout, r.Stderr, r.ReturnCode
	}
	return payload, r.Stdout, r.Stderr, r.ReturnCode
}

func pollInspect(ctx *runner.Context, name string, timeout time.Duration) (map[string]any, []byte, []byte, int) {
	deadline := time.Now().Add(timeout)
	var last map[string]any
	var so, se []byte
	rc := 1
	for time.Now().Before(deadline) {
		payload, o, e, code := inspect(ctx, name)
		so, se, rc = o, e, code
		if m, ok := payload.(map[string]any); ok {
			last = m
			if util.GetAny(m, fieldAliases(ctx, "exit_code")) != nil || util.GetAny(m, fieldAliases(ctx, "signal")) != nil || stateIs(util.GetAny(m, fieldAliases(ctx, "state")), "exited", "completed", "finished", "terminated", "signaled", "signalled", "failed", "failed_to_start", "start_failed", "exec_failed") {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return last, so, se, rc
}

func stage18(ctx *runner.Context) []model.CheckResult {
	var results []model.CheckResult
	for _, expected := range []int{0, 7} {
		results = append(results, single(ctx, fmt.Sprintf("inspect preserves normal exit %d with a terminal state", expected), func() (bool, string, []byte, []byte) {
			name := fmt.Sprintf("verify-%d-exit%d", os.Getpid(), expected)
			created := ctx.NewSession(name, []string{"sh", "-c", fmt.Sprintf("exit %d", expected)}, ctx.Timeout)
			if !created.Succeeded() {
				return false, "session creation failed", created.Stdout, created.Stderr
			}
			record, so, se, rc := pollInspect(ctx, name, ctx.Timeout)
			return rc == 0 && normalExit(ctx, record, expected), fmt.Sprintf("record=%v", record), so, se
		}))
	}
	results = append(results, single(ctx, "inspect identifies SIGTERM distinctly", func() (bool, string, []byte, []byte) {
		name := fmt.Sprintf("verify-%d-sigterm", os.Getpid())
		created := ctx.NewSession(name, []string{"sh", "-c", "kill -TERM $$"}, ctx.Timeout)
		if !created.Succeeded() {
			return false, "session creation failed", created.Stdout, created.Stderr
		}
		record, so, se, rc := pollInspect(ctx, name, ctx.Timeout)
		return rc == 0 && signalExit(ctx, record), fmt.Sprintf("record=%v", record), so, se
	}))
	results = append(results, single(ctx, "inspect retains failed-before-execution records", func() (bool, string, []byte, []byte) {
		name := fmt.Sprintf("verify-%d-badexec", os.Getpid())
		created := ctx.NewSession(name, []string{"/definitely/missing/dmux-verifier-executable"}, ctx.Timeout)
		if created.TimedOut || created.Error != "" {
			return false, "creation must finish without a verifier timeout or launch error", created.Stdout, created.Stderr
		}
		record, so, se, rc := pollInspect(ctx, name, ctx.Timeout)
		valid := stateIs(util.GetAny(record, fieldAliases(ctx, "state")), "failed", "failed_to_start", "start_failed", "exec_failed")
		return rc == 0 && valid, fmt.Sprintf("failed-before-execution record required; record=%v", record), so, se
	}))
	results = append(results, single(ctx, "inspect distinguishes a running command from terminal records", func() (bool, string, []byte, []byte) {
		name := fmt.Sprintf("verify-%d-running", os.Getpid())
		pidfile := filepath.Join(ctx.TempDir, "inspect-running.pid")
		created := ctx.NewSession(name, writePIDCommand(pidfile, false), ctx.Timeout)
		pid, alive := livePID(pidfile, ctx.Timeout)
		if !created.Succeeded() || !alive {
			return false, "command must be live before inspect", created.Stdout, created.Stderr
		}
		payload, so, se, rc := inspect(ctx, name)
		record, _ := payload.(map[string]any)
		valid := stateIs(util.GetAny(record, fieldAliases(ctx, "state")), "running", "alive") && util.GetAny(record, fieldAliases(ctx, "exit_code")) == nil && util.GetAny(record, fieldAliases(ctx, "signal")) == nil
		return rc == 0 && valid && util.ProcessAlive(pid), fmt.Sprintf("running record=%v", record), so, se
	}))
	return results
}
