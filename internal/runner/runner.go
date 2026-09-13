package runner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Alisjj/durablemux-verifier/internal/model"
	"github.com/Alisjj/durablemux-verifier/internal/ptyproc"
	"github.com/Alisjj/durablemux-verifier/internal/util"
)

// CommandOutput mirrors Python runner.CommandOutput.
type CommandOutput struct {
	Argv       []string
	ReturnCode int
	Stdout     []byte
	Stderr     []byte
	Duration   time.Duration
}

// Context is an isolated verification run: private temp runtime,
// templated env, tracked sessions, guaranteed cleanup.
type Context struct {
	Project  string
	Config   map[string]any
	Workdir  string
	Binary   string
	Timeout  time.Duration
	TempDir  string
	Runtime  string
	Env      []string
	envMap   map[string]string
	Sessions map[string]bool
}

// New creates temp dirs and env. Call Close when done.
func New(project string, config map[string]any) (*Context, error) {
	proj, err := filepath.Abs(project)
	if err != nil {
		return nil, err
	}
	workRel, _ := config["working_directory"].(string)
	if workRel == "" {
		workRel = "."
	}
	workdir := workRel
	if !filepath.IsAbs(workdir) {
		workdir = filepath.Join(proj, workRel)
	}
	rawBin, _ := config["binary"].(string)
	if rawBin == "" {
		rawBin = "./dmux"
	}
	bin := rawBin
	if !filepath.IsAbs(bin) {
		bin = filepath.Join(proj, rawBin)
	}
	timeout := 8 * time.Second
	if f, ok := numField(config, "timeout_seconds"); ok {
		timeout = time.Duration(f * float64(time.Second))
	}
	tmp, err := os.MkdirTemp("", "dmux-verifier-")
	if err != nil {
		return nil, err
	}
	rt := filepath.Join(tmp, "runtime")
	if err := os.MkdirAll(rt, 0o700); err != nil {
		os.RemoveAll(tmp)
		return nil, err
	}
	envMap := map[string]string{}
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			envMap[kv[:i]] = kv[i+1:]
		}
	}
	// Never inherit the caller's real session runtime when isolation settings
	// are missing or explicitly removed from a partial configuration.
	envMap["XDG_RUNTIME_DIR"] = rt
	envMap["DMUX_RUNTIME_DIR"] = filepath.Join(rt, "dmux")
	if re, ok := config["runtime_environment"].(map[string]any); ok {
		for k, v := range re {
			s, _ := v.(string)
			s = strings.ReplaceAll(s, "{temp_runtime}", rt)
			envMap[k] = s
		}
	}
	for _, key := range []string{"XDG_RUNTIME_DIR", "DMUX_RUNTIME_DIR"} {
		path := envMap[key]
		if !withinDir(tmp, path) {
			os.RemoveAll(tmp)
			return nil, fmt.Errorf("%s must stay inside the temporary runtime: %s", key, path)
		}
		if err := os.MkdirAll(path, 0o700); err != nil {
			os.RemoveAll(tmp)
			return nil, err
		}
	}
	env := make([]string, 0, len(envMap))
	for k, v := range envMap {
		env = append(env, k+"="+v)
	}
	return &Context{
		Project: proj, Config: config, Workdir: workdir, Binary: bin,
		Timeout: timeout, TempDir: tmp, Runtime: rt,
		Env: env, envMap: envMap, Sessions: map[string]bool{},
	}, nil
}

func withinDir(base, target string) bool {
	if target == "" || !filepath.IsAbs(target) {
		return false
	}
	rel, err := filepath.Rel(base, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// Close kills sessions, stops daemon, cleans runtime procs and temp dir.
func (c *Context) Close() {
	for name := range c.Sessions {
		_ = c.RunAction("kill", map[string]string{"name": name}, nil, nil, 2*time.Second)
	}
	if _, ok := c.commands()["daemon_stop"]; ok {
		_ = c.RunAction("daemon_stop", nil, nil, nil, 2*time.Second)
	}
	util.CleanupRuntimeProcesses(c.envMap["XDG_RUNTIME_DIR"], c.envMap["DMUX_RUNTIME_DIR"])
	os.RemoveAll(c.TempDir)
}

func (c *Context) commands() map[string]any {
	m, _ := c.Config["commands"].(map[string]any)
	return m
}

// Command expands a configured template, e.g. new -> [binary new {name} --].
func (c *Context) Command(action string, values map[string]string) ([]string, error) {
	tmpl, ok := c.commands()[action]
	if !ok || tmpl == nil {
		return nil, fmt.Errorf("no command template configured for %q", action)
	}
	arr, ok := tmpl.([]any)
	if !ok {
		return nil, fmt.Errorf("bad command template for %q", action)
	}
	argv := []string{c.Binary}
	for _, p := range arr {
		s, _ := p.(string)
		for k, v := range values {
			s = strings.ReplaceAll(s, "{"+k+"}", v)
		}
		argv = append(argv, s)
	}
	return argv, nil
}

// RunAction runs a configured action with extra args appended.
func (c *Context) RunAction(action string, values map[string]string, extra []string, input []byte, timeout time.Duration) CommandOutput {
	argv, err := c.Command(action, values)
	if err != nil {
		return CommandOutput{Argv: []string{action}, ReturnCode: 127, Stderr: []byte(err.Error())}
	}
	argv = append(argv, extra...)
	return c.Run(argv, input, timeout, "")
}

// Run executes argv with env + timeout. Timeout expiry yields exit 124.
func (c *Context) Run(argv []string, input []byte, timeout time.Duration, cwd string) CommandOutput {
	if len(argv) == 0 || argv[0] == "" {
		return CommandOutput{Argv: argv, ReturnCode: 127, Stderr: []byte("command must contain an executable")}
	}
	if timeout <= 0 {
		timeout = c.Timeout
	}
	if cwd == "" {
		cwd = c.Workdir
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// A broken command can leave descendants holding the output pipes open.
	// Put each invocation in its own process group, kill that group on timeout,
	// and bound the final pipe wait even if a descendant escapes the group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 500 * time.Millisecond
	cmd.Dir = cwd
	cmd.Env = c.Env
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err := cmd.Run()
	dur := time.Since(start)
	rc := 0
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			rc = 124
		} else if ee, ok := err.(*exec.ExitError); ok {
			rc = ee.ExitCode()
		} else {
			rc = 127
		}
	}
	// exec truncates output on kill; buffers hold what we got.
	return CommandOutput{Argv: argv, ReturnCode: rc, Stdout: so.Bytes(), Stderr: se.Bytes(), Duration: dur}
}

// Environment returns one environment value configured for this run.
func (c *Context) Environment(name string) string {
	return c.envMap[name]
}

// SpawnPTY starts action under a PTY.
func (c *Context) SpawnPTY(action string, values map[string]string, extra []string, rows, cols int) (*ptyproc.Process, error) {
	argv, err := c.Command(action, values)
	if err != nil {
		return nil, err
	}
	argv = append(argv, extra...)
	return ptyproc.Start(argv, c.Workdir, c.Env, rows, cols)
}

// NewSession tracks name then runs the new action.
func (c *Context) NewSession(name string, command []string, timeout time.Duration) CommandOutput {
	c.Sessions[name] = true
	return c.RunAction("new", map[string]string{"name": name}, command, nil, timeout)
}

// Result builds a timed CheckResult.
func (c *Context) Result(name string, passed bool, detail string, start time.Time, stdout, stderr []byte) model.CheckResult {
	return model.CheckResult{
		Name: name, Passed: passed, Detail: detail,
		DurationSeconds: time.Since(start).Seconds(),
		Stdout:          string(stdout), Stderr: string(stderr),
	}
}

func numField(cfg map[string]any, key string) (float64, bool) {
	v, ok := cfg[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	}
	return 0, false
}
