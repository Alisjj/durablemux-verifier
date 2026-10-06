// Package proctree captures a real foreground command's process relationships.
package proctree

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Alisjj/durablemux-verifier/internal/ptyproc"
	"github.com/Alisjj/durablemux-verifier/internal/runner"
	"github.com/Alisjj/durablemux-verifier/internal/util"
	"golang.org/x/sys/unix"
)

type Process struct {
	PID, PPID, PGID, SID, TPGID int
	State, TTY, Command         string
}

func Capture(ctx *runner.Context) ([]byte, error) {
	argv, err := ctx.Command("run", nil)
	if err != nil {
		return nil, err
	}
	argv = append(argv, "sleep", "30")
	var quoted []string
	for _, arg := range argv {
		quoted = append(quoted, "'"+strings.ReplaceAll(arg, "'", "'\\''")+"'")
	}
	command := strings.Join(quoted, " ")
	const prompt = "__DMUX_PROCESS_TREE_SHELL__ "
	var env []string
	for _, value := range ctx.Env {
		key, _, _ := strings.Cut(value, "=")
		switch key {
		case "PS1", "PROMPT_COMMAND", "BASH_ENV", "ENV":
			continue
		}
		env = append(env, value)
	}
	env = append(env, "PS1="+prompt)
	shell, err := ptyproc.Start([]string{"bash", "--noprofile", "--norc", "-i"}, ctx.Workdir, env, 24, 80)
	if err != nil {
		return nil, err
	}
	var observed []Process
	defer func() { cleanup(ctx, shell, observed) }()
	if _, err := shell.ReadUntil([]byte(prompt), ctx.Timeout); err != nil {
		return nil, fmt.Errorf("invoking shell did not become ready: %w", err)
	}
	start := len(shell.Buffer)
	if err := shell.Write([]byte(command + "\n")); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(ctx.Timeout)
	var commandPID int
	for time.Now().Before(deadline) {
		rows, err := tree(ctx, shell.Pid)
		if err != nil {
			return nil, err
		}
		observed = rows
		for _, process := range rows {
			args := strings.Fields(process.Command)
			if !strings.HasPrefix(process.State, "Z") && len(args) == 2 && filepath.Base(args[0]) == "sleep" && args[1] == "30" {
				commandPID = process.PID
				break
			}
		}
		if commandPID != 0 {
			break
		}
		shell.ReadSome(30 * time.Millisecond)
		if shell.Poll() != nil || strings.Contains(string(shell.Buffer[start:]), prompt) {
			return nil, fmt.Errorf("dmux returned before a running sleep 30 could be captured; terminal output:\n%s", shell.Buffer[start:])
		}
	}
	if commandPID == 0 {
		return nil, fmt.Errorf("did not observe sleep 30 in the invoking shell's process tree within %s", ctx.Timeout)
	}
	var output strings.Builder
	fmt.Fprintf(&output, "$ %s\n\nCaptured at %s while sleep 30 was active.\nSID values come from getsid(PID), not macOS ps sess pointers.\n\n", command, time.Now().UTC().Format(time.RFC3339Nano))
	fmt.Fprintln(&output, "PID PPID PGID SID TPGID STAT TTY COMMAND")
	for _, process := range observed {
		fmt.Fprintf(&output, "%d %d %d %d %d %s %s %s\n", process.PID, process.PPID, process.PGID, process.SID, process.TPGID, process.State, process.TTY, process.Command)
	}
	fmt.Fprintf(&output, "\nInvoking shell PID: %d\nCommand PID: %d\n", shell.Pid, commandPID)
	for _, process := range observed {
		if process.PPID == shell.Pid {
			fmt.Fprintf(&output, "dmux invocation PID: %d\n", process.PID)
			if process.PID == commandPID {
				fmt.Fprintln(&output, "The invocation has exec-replaced itself with sleep; the PID is shared. Explain this observation rather than inventing a separate dmux process.")
			}
		}
	}
	fmt.Fprintln(&output, "\n[standard descriptors]")
	writeDescriptors(ctx, &output, observed)
	current, err := tree(ctx, shell.Pid)
	if err != nil {
		return nil, err
	}
	stillRunning := false
	for _, process := range current {
		if process.PID == commandPID && !strings.HasPrefix(process.State, "Z") && util.ProcessAlive(commandPID) {
			stillRunning = true
		}
	}
	if !stillRunning {
		return nil, fmt.Errorf("sleep 30 left the invocation's process tree before capture finished; no passing snapshot was recorded")
	}
	return []byte(output.String()), nil
}

func snapshot(ctx *runner.Context) ([]Process, error) {
	// These ps fields work on both Linux and Darwin; SID is queried separately.
	r := ctx.Run([]string{"ps", "-Aww", "-o", "pid=,ppid=,pgid=,tpgid=,stat=,tty=,command="}, nil, 2*time.Second, "")
	if !r.Succeeded() {
		return nil, fmt.Errorf("process snapshot failed: exit=%d; %s%s", r.ReturnCode, r.Error, r.Stderr)
	}
	var all []Process
	for _, line := range strings.Split(string(r.Stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 7 {
			continue
		}
		var numbers [4]int
		valid := true
		for i := range numbers {
			var err error
			numbers[i], err = strconv.Atoi(fields[i])
			valid = valid && err == nil
		}
		if !valid {
			continue
		}
		all = append(all, Process{PID: numbers[0], PPID: numbers[1], PGID: numbers[2], TPGID: numbers[3], State: fields[4], TTY: fields[5], Command: strings.Join(fields[6:], " ")})
	}
	return all, nil
}

func tree(ctx *runner.Context, shellPID int) ([]Process, error) {
	all, err := snapshot(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[int]bool{shellPID: true}
	var selected []Process
	for _, process := range all {
		if process.PID == shellPID {
			selected = append(selected, process)
			break
		}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("invoking shell exited before capture")
	}
	for changed := true; changed; {
		changed = false
		for _, process := range all {
			if seen[process.PPID] && !seen[process.PID] {
				selected = append(selected, process)
				seen[process.PID] = true
				changed = true
			}
		}
	}
	for i := range selected {
		sid, err := unix.Getsid(selected[i].PID)
		if err != nil {
			return nil, fmt.Errorf("process %d exited before its SID could be captured: %w", selected[i].PID, err)
		}
		selected[i].SID = sid
	}
	return selected, nil
}

func cleanup(ctx *runner.Context, shell *ptyproc.Process, observed []Process) {
	// Include orphans still in the isolated invoking shell's session, even if
	// a broken dmux has already returned. Other sessions are selected only when
	// they were previously observed as descendants of this experiment.
	known := map[int]Process{}
	for _, process := range observed {
		known[process.PID] = process
	}
	if all, err := snapshot(ctx); err == nil {
		for _, process := range all {
			sid, err := unix.Getsid(process.PID)
			previous, tracked := known[process.PID]
			if err == nil && (sid == shell.Pid || (tracked && sid == previous.SID && process.PGID == previous.PGID)) {
				process.SID = sid
				known[process.PID] = process
			}
		}
	}
	signalProcess := func(process Process, sig unix.Signal) {
		if process.PID == shell.Pid {
			return
		}
		// Recheck identity before signalling a PID saved by an earlier snapshot.
		sid, sidErr := unix.Getsid(process.PID)
		pgid, pgidErr := unix.Getpgid(process.PID)
		if sidErr == nil && pgidErr == nil && sid == process.SID && pgid == process.PGID {
			_ = unix.Kill(process.PID, sig)
		}
	}
	parents := map[int]bool{}
	for _, process := range known {
		parents[process.PPID] = true
	}
	// Give waiting parents a chance to reap their children before stopping any
	// remaining processes, including implementations that ignore SIGTERM.
	for _, process := range known {
		if !parents[process.PID] {
			signalProcess(process, unix.SIGTERM)
		}
	}
	shell.ReadSome(100 * time.Millisecond)
	for _, process := range known {
		signalProcess(process, unix.SIGTERM)
	}
	_ = shell.Write([]byte("exit\n"))
	_, _ = shell.Wait(300 * time.Millisecond)
	for _, process := range known {
		signalProcess(process, unix.SIGKILL)
	}
	shell.Terminate()
}

func writeDescriptors(ctx *runner.Context, output *strings.Builder, rows []Process) {
	if _, err := os.Stat("/proc/self/fd"); err == nil {
		for _, process := range rows {
			for fd := 0; fd < 3; fd++ {
				target, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%d", process.PID, fd))
				if err != nil {
					target = "unavailable: " + err.Error()
				}
				fmt.Fprintf(output, "PID %d fd %d -> %s\n", process.PID, fd, target)
			}
		}
		return
	}
	var pids []string
	for _, process := range rows {
		pids = append(pids, strconv.Itoa(process.PID))
	}
	r := ctx.Run([]string{"lsof", "-nP", "-a", "-p", strings.Join(pids, ","), "-d", "0,1,2"}, nil, 2*time.Second, "")
	if !r.Succeeded() {
		fmt.Fprintf(output, "Descriptor capture unavailable: %s%s (exit %d). Add descriptor observations separately.\n", r.Error, r.Stderr, r.ReturnCode)
		return
	}
	output.Write(r.Stdout)
}
