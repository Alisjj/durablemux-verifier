package checks

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Alisjj/durablemux-verifier/internal/runner"
	"github.com/Alisjj/durablemux-verifier/internal/util"
)

func detachProbe(ctx *runner.Context) (bool, string, []byte, []byte) {
	name := fmt.Sprintf("verify-%d-detach", os.Getpid())
	parentFile := filepath.Join(ctx.TempDir, "detach-shell.pid")
	childFile := filepath.Join(ctx.TempDir, "detach-job.pid")
	inputFile := filepath.Join(ctx.TempDir, "detach-input")
	code := fmt.Sprintf(`import os,tty
open(%q,'w').write(str(os.getpid()))
tty.setraw(0)
with open(%q,'wb',buffering=0) as recorded:
 while True:
  b=os.read(0,1)
  if not b: break
  recorded.write(b)
  os.write(1,('__BYTE__'+b.hex()).encode())
`, childFile, inputFile)
	// Bash runs the byte probe as a real foreground job on the session PTY.
	script := fmt.Sprintf("echo $$ > %s; python3 -u -c %s", shellQuote(parentFile), shellQuote(code))
	created := ctx.NewSession(name, []string{"bash", "--noprofile", "--norc", "-ic", script}, ctx.Timeout)
	parent, parentAlive := livePID(parentFile, ctx.Timeout)
	child, childAlive := livePID(childFile, ctx.Timeout)
	if !created.Succeeded() || !parentAlive || !childAlive {
		return false, "shell and foreground job must be live before attachment", created.Stdout, created.Stderr
	}
	p, err := ctx.SpawnPTY("attach", map[string]string{"name": name}, nil, 24, 80)
	if err != nil {
		return false, err.Error(), nil, nil
	}
	defer p.Terminate()
	if err := p.Write([]byte("Q")); err != nil {
		return false, err.Error(), nil, nil
	}
	if data, err := p.ReadUntil([]byte("__BYTE__51"), ctx.Timeout); err != nil || p.Poll() != nil {
		return false, "attachment must deliver input to the live foreground job", data, nil
	}
	seq, _ := ctx.Config["detach_sequence"].(string)
	if seq == "" {
		return false, "detach_sequence must be configured", p.Buffer, nil
	}
	if err := p.Write([]byte(seq)); err != nil {
		return false, err.Error(), p.Buffer, nil
	}
	rc, err := p.Wait(ctx.Timeout)
	recorded, readErr := os.ReadFile(inputFile)
	restored, restoreErr := p.TerminalRestored()
	if err != nil || rc != 0 || readErr != nil || !bytes.Equal(recorded, []byte("Q")) || !restored || restoreErr != nil || !util.ProcessAlive(parent) || !util.ProcessAlive(child) {
		return false, fmt.Sprintf("detach exit=%d; error=%v; child input=%q; terminal restored=%v; shell/job must survive", rc, err, recorded, restored), p.Buffer, nil
	}
	reconnected, err := ctx.SpawnPTY("attach", map[string]string{"name": name}, nil, 24, 80)
	if err != nil {
		return false, err.Error(), p.Buffer, nil
	}
	defer reconnected.Terminate()
	literal, _ := ctx.Config["literal_prefix_sequence"].(string)
	if literal == "" {
		return false, "literal_prefix_sequence must describe the prefix escape mechanism", nil, nil
	}
	if err := reconnected.Write([]byte(literal + "R")); err != nil {
		return false, err.Error(), nil, nil
	}
	data, err := reconnected.ReadUntil([]byte("__BYTE__52"), ctx.Timeout)
	recorded, readErr = os.ReadFile(inputFile)
	expected := append([]byte{'Q', seq[0]}, 'R')
	ok := err == nil && readErr == nil && bytes.Equal(recorded, expected) && util.ProcessAlive(parent) && util.ProcessAlive(child)
	return ok, fmt.Sprintf("reattachment and literal prefix delivery; child input=%q; expected=%q; error=%v", recorded, expected, err), data, nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

func validateLiveSession(ctx *runner.Context, item map[string]any, name string) error {
	get := func(field string) any { return util.GetAny(item, fieldAliases(ctx, field)) }
	id, isString := get("id").(string)
	number, isInteger := integer(get("id"))
	if !(isString && strings.TrimSpace(id) != "") && !(isInteger && number > 0) {
		return fmt.Errorf("%s.id must be a non-empty string or positive integer", name)
	}
	if get("name") != name {
		return fmt.Errorf("%s.name must match the created session", name)
	}
	command := ""
	switch v := get("command").(type) {
	case string:
		command = v
	case []any:
		for _, arg := range v {
			s, ok := arg.(string)
			if !ok {
				return fmt.Errorf("%s.command must contain string arguments", name)
			}
			command += " " + s
		}
	}
	args := strings.Fields(command)
	if len(args) != 2 || filepath.Base(args[0]) != "sleep" || args[1] != "60" {
		return fmt.Errorf("%s.command must describe the requested sleep 60 command", name)
	}
	created := get("created_at")
	validTime := false
	if s, ok := created.(string); ok {
		t, err := time.Parse(time.RFC3339Nano, s)
		validTime = err == nil && !t.IsZero()
	} else if n, ok := created.(float64); ok {
		validTime = n > 0
	}
	if !validTime {
		return fmt.Errorf("%s.created_at must be an RFC3339 timestamp or positive Unix timestamp", name)
	}
	count, ok := integer(get("attached_clients"))
	if !ok || count != 0 {
		return fmt.Errorf("%s.attached_clients must be zero for an unattached session", name)
	}
	if !stateIs(get("state"), "running", "alive") {
		return fmt.Errorf("%s.state must report a running command", name)
	}
	return nil
}

func stableSessionIDs(ctx *runner.Context, before map[string]map[string]any, after []map[string]any) bool {
	if len(before) != len(after) {
		return false
	}
	seen := map[string]bool{}
	for _, row := range after {
		name, ok := util.GetAny(row, fieldAliases(ctx, "name")).(string)
		if !ok || seen[name] || before[name] == nil || util.GetAny(row, fieldAliases(ctx, "id")) != util.GetAny(before[name], fieldAliases(ctx, "id")) {
			return false
		}
		seen[name] = true
	}
	return true
}

func normalExit(ctx *runner.Context, record map[string]any, expected int) bool {
	n, ok := integer(util.GetAny(record, fieldAliases(ctx, "exit_code")))
	return ok && n == expected && stateIs(util.GetAny(record, fieldAliases(ctx, "state")), "exited", "completed", "finished") && util.GetAny(record, fieldAliases(ctx, "signal")) == nil
}

func signalExit(ctx *runner.Context, record map[string]any) bool {
	if !stateIs(util.GetAny(record, fieldAliases(ctx, "state")), "exited", "terminated", "signaled", "signalled") {
		return false
	}
	sig := util.GetAny(record, fieldAliases(ctx, "signal"))
	validSignal := sig == float64(15) || sig == "SIGTERM" || sig == "TERM" || sig == "sigterm" || sig == "term"
	code := util.GetAny(record, fieldAliases(ctx, "exit_code"))
	return validSignal && (code == nil || code == float64(-1) || code == float64(143))
}
