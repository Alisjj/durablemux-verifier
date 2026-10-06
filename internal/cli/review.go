package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Alisjj/durablemux-verifier/internal/ptyproc"
	"github.com/Alisjj/durablemux-verifier/internal/review"
	"github.com/Alisjj/durablemux-verifier/internal/runner"
	"github.com/Alisjj/durablemux-verifier/internal/store"
	"github.com/Alisjj/durablemux-verifier/internal/util"
	"github.com/creack/pty"
)

type reviewUI struct {
	input  io.Reader
	output io.Writer
}

func (ui *reviewUI) prompt(text string) (string, error) {
	fmt.Fprint(ui.output, text)
	// Do not read ahead: pasted live-command input must remain on the terminal
	// for Interact rather than getting stranded in a buffered prompt reader.
	var line strings.Builder
	var b [1]byte
	for {
		_, err := io.ReadFull(ui.input, b[:])
		if err != nil {
			if err == io.EOF && line.Len() > 0 {
				return strings.TrimSpace(line.String()), nil
			}
			return "", err
		}
		if b[0] == '\n' {
			return strings.TrimSpace(line.String()), nil
		}
		line.WriteByte(b[0])
	}
}

func cmdReview(project string, args []string, input io.Reader, output io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(output, "usage: review <stage|next>")
		return 2
	}
	root, meta, cfg, stages, s, err := loadAll(project)
	if err != nil {
		fmt.Fprintln(output, err)
		return 1
	}
	n, err := resolveStage(args[0], stages, s)
	if err != nil {
		fmt.Fprintln(output, err)
		return 2
	}
	stage := stages[n]
	if stage.Mode == "auto" {
		fmt.Fprintf(output, "Stage %d is automated. Run dmux-verify verify %d.\n", n, n)
		return 2
	}
	ctx, err := runner.New(root, cfg)
	if err != nil {
		fmt.Fprintln(output, err)
		return 1
	}
	defer ctx.Close()
	sha, err := util.Sha256File(ctx.Binary)
	if err != nil {
		fmt.Fprintln(output, err)
		return 1
	}
	ui := &reviewUI{input: input, output: output}
	fmt.Fprintf(output, "Stage %d: %s [%s]\n\n", n, stage.Title, stage.Mode)
	fmt.Fprintf(output, "Binary: %s\nPrivate runtime: %s\nUse {binary} in captured commands for the configured executable.\n", ctx.Binary, ctx.Runtime)
	results, err := runStageChecks(stage, ctx)
	if err != nil {
		fmt.Fprintln(output, err)
		return 1
	}
	autoPassed := true
	for _, result := range results {
		label := "PASS"
		if !result.Passed {
			label, autoPassed = "FAIL", false
		}
		fmt.Fprintf(output, "%s  %s — %s\n", label, result.Name, result.Detail)
	}
	// Review captures share a fresh runtime, independent of automated probes.
	ctx.Close()
	ctx, err = runner.New(root, cfg)
	if err != nil {
		fmt.Fprintln(output, err)
		return 1
	}
	ctx.DiscoverSessions = true
	defer ctx.Close()
	fmt.Fprintf(output, "\nReview runtime: %s\nReview session: %s\nCommands captured during this review share this runtime. Use {session} for sessions to clean up when review ends.\n", ctx.Runtime, ctx.ReviewSessionName())
	criteria := stage.ReviewCriteria()
	items := map[string]any{}
	resuming := false
	if previous, ok := s.Stage(n)["review"].(map[string]any); ok && previous["binary_sha256"] == sha {
		if saved, ok := previous["requirements"].(map[string]any); ok {
			for id, item := range saved {
				items[id] = item
			}
			resuming = true
		}
	}
	record := map[string]any{"at": store.UTCNow(), "binary_sha256": sha, "requirements": items}
	save := func() error { record["at"] = store.UTCNow(); return s.RecordReview(n, record) }
	if !resuming || (s.Approved(n) && review.Complete(stage, s, root, sha) != nil) {
		if err := save(); err != nil {
			fmt.Fprintln(output, err)
			return 1
		}
	}
	if n == 4 {
		fmt.Fprintln(output, "Press t to run the process-tree experiment: capture shell, dmux invocation, sleep 30 and standard descriptors while the command is alive. The experiment returns automatically; then explain the observations.")
	}
	for i, text := range criteria {
		id := review.CriterionID(text)
		fmt.Fprintf(output, "\n[%d/%d] %s\n", i+1, len(criteria), text)
		previous, _ := items[id].(map[string]any)
		keepSaved := previous != nil
		if previous != nil {
			fmt.Fprintf(output, "Saved result: %v — %v\n", previous["result"], previous["note"])
			if previous["result"] == "pass" {
				path, _ := previous["evidence"].(string)
				if err := review.CriterionEvidenceValid(stage, text, s, root, path); err != nil {
					keepSaved = false
					fmt.Fprintf(output, "Saved evidence needs attention: %s\nCapture or select valid evidence and record the result again.\n", err)
				}
			}
		}
		selectedEvidence := ""
		for {
			prompt := "Result: [p]ass, [f]ail, [s]kip, [q]uit; collect evidence first with [c]apture, [l]ive or [a]rtefact"
			if n == 4 {
				prompt += ", [t]ree experiment"
			}
			answer, err := ui.prompt(prompt + " (Enter keeps saved result): ")
			if err != nil || strings.EqualFold(answer, "q") {
				fmt.Fprintln(output, "Review saved; resume with the same review command.")
				return 1
			}
			if answer == "" && previous != nil {
				if keepSaved {
					break
				}
				fmt.Fprintln(output, "The saved pass needs valid evidence before it can be kept.")
				continue
			}
			if n == 4 && strings.EqualFold(answer, "t") {
				path, err := ui.captureProcessTree(ctx, s, root, meta)
				if err != nil {
					fmt.Fprintln(output, err)
					continue
				}
				selectedEvidence = path
				fmt.Fprintf(output, "Recorded evidence: %s\nNow record the observed result.\n", path)
				continue
			}
			if kind := strings.ToLower(answer); kind == "c" || kind == "l" || kind == "a" {
				prompt := "Command: "
				if kind == "a" {
					kind, prompt = "f", "File path (relative to project): "
				}
				value, err := ui.prompt(prompt)
				if err != nil {
					return 1
				}
				path, err := ui.captureEvidence(ctx, s, root, meta, n, kind, value)
				if err != nil {
					fmt.Fprintln(output, err)
					continue
				}
				selectedEvidence = path
				fmt.Fprintf(output, "Recorded evidence: %s\nNow record the observed result.\n", path)
				continue
			}
			result := ""
			switch strings.ToLower(answer) {
			case "p", "pass":
				result = "pass"
			case "f", "fail":
				result = "fail"
			case "s", "skip", "":
				result = "skip"
			default:
				fmt.Fprintln(output, "Choose pass, fail, skip or quit.")
				continue
			}
			item := map[string]any{"text": text, "result": result, "at": store.UTCNow()}
			if result != "skip" {
				note, err := ui.prompt("Observation or explanation: ")
				if err != nil {
					return 1
				}
				if note == "" {
					fmt.Fprintln(output, "Record an observation before marking a result.")
					continue
				}
				item["note"] = note
			}
			if result == "pass" {
				if selectedEvidence == "" {
					path, err := ui.selectEvidence(ctx, s, root, meta, n)
					if err != nil {
						fmt.Fprintln(output, err)
						return 1
					}
					selectedEvidence = path
				}
				if err := review.CriterionEvidenceValid(stage, text, s, root, selectedEvidence); err != nil {
					fmt.Fprintln(output, err)
					selectedEvidence = ""
					continue
				}
				item["evidence"] = selectedEvidence
			} else if selectedEvidence != "" {
				item["evidence"] = selectedEvidence
			}
			items[id] = item
			if err := save(); err != nil {
				fmt.Fprintln(output, err)
				return 1
			}
			break
		}
	}
	if err := review.Complete(stage, s, root, sha); err != nil {
		fmt.Fprintf(output, "\nReview incomplete: %s\n", err)
		return 1
	}
	if count := review.EvidenceCount(s, n, root); count < stage.MinimumEvidence {
		fmt.Fprintf(output, "Stage %d requires %d intact evidence items; found %d. Add evidence and resume review.\n", n, stage.MinimumEvidence, count)
		return 1
	}
	if !autoPassed {
		fmt.Fprintln(output, "Review saved. Fix the failed automated checks before approval.")
		return 1
	}
	answer, err := ui.prompt("\nApprove all reviewed requirements? [y/N]: ")
	if err != nil || !strings.EqualFold(answer, "y") {
		if s.Approved(n) {
			fmt.Fprintln(output, "Review unchanged; existing approval retained.")
		} else {
			fmt.Fprintln(output, "Review saved without approval.")
		}
		return 1
	}
	if err := s.Approve(n, fmt.Sprintf("Guided review of all %d requirements with recorded observations and evidence", len(criteria))); err != nil {
		fmt.Fprintln(output, err)
		return 1
	}
	fmt.Fprintf(output, "Review approved. Run dmux-verify verify %d to complete the stage.\n", n)
	return 0
}

func (ui *reviewUI) selectEvidence(ctx *runner.Context, s *store.Store, root, meta string, stage int) (string, error) {
	for {
		evidence, _ := s.Stage(stage)["evidence"].([]any)
		fmt.Fprintln(ui.output, "Evidence:")
		for i, value := range evidence {
			ev, _ := value.(map[string]any)
			fmt.Fprintf(ui.output, "  %d. %v — %v\n", i+1, ev["path"], ev["note"])
		}
		prompt := "Choose a number, [c]apture command, [l]ive terminal, [f]ile, or [q]uit"
		if stage == 4 {
			prompt += ", [t]ree experiment"
		}
		answer, err := ui.prompt(prompt + ": ")
		if err != nil {
			return "", err
		}
		if strings.EqualFold(answer, "q") {
			return "", fmt.Errorf("Review saved; resume with the same review command.")
		}
		if i, err := strconv.Atoi(answer); err == nil && i > 0 && i <= len(evidence) {
			ev, _ := evidence[i-1].(map[string]any)
			path, _ := ev["path"].(string)
			if err := review.EvidenceValid(s, stage, root, path); err != nil {
				fmt.Fprintln(ui.output, err)
				continue
			}
			return path, nil
		}
		kind := strings.ToLower(answer)
		if stage == 4 && kind == "t" {
			path, err := ui.captureProcessTree(ctx, s, root, meta)
			if err != nil {
				fmt.Fprintln(ui.output, err)
				continue
			}
			return path, nil
		}
		if kind != "c" && kind != "l" && kind != "f" {
			fmt.Fprintln(ui.output, "Select evidence, a capture option, or quit.")
			continue
		}
		prompt = "Command: "
		if kind == "f" {
			prompt = "File path (relative to project): "
		}
		value, err := ui.prompt(prompt)
		if err != nil {
			return "", err
		}
		if value == "" {
			continue
		}
		path, err := ui.captureEvidence(ctx, s, root, meta, stage, kind, value)
		if err != nil {
			fmt.Fprintln(ui.output, err)
			continue
		}
		return path, nil
	}
}

func (ui *reviewUI) captureEvidence(ctx *runner.Context, s *store.Store, root, meta string, stage int, kind, value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("provide a command or artefact path")
	}
	dir := filepath.Join(meta, "evidence", fmt.Sprintf("stage-%02d", stage))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	target := filepath.Join(dir, stamp+"-review.txt")
	record := map[string]any{"at": store.UTCNow(), "note": "Guided review evidence", "type": "command"}
	if kind == "f" {
		source := value
		if !filepath.IsAbs(source) {
			source = filepath.Join(root, source)
		}
		target = filepath.Join(dir, stamp+"-"+filepath.Base(source))
		if err := copyFile(source, target); err != nil {
			return "", err
		}
		record["type"] = "file"
	} else {
		command := expandEvidenceCommand(ctx, value)
		rc, timedOut, runError := 0, false, ""
		var stdout, stderr []byte
		if kind == "l" {
			input, ok := ui.input.(*os.File)
			if !ok {
				return "", fmt.Errorf("live capture requires a terminal")
			}
			size, err := pty.GetsizeFull(input)
			if err != nil {
				return "", fmt.Errorf("live capture requires a terminal: %w", err)
			}
			p, err := ptyproc.Start([]string{"sh", "-lc", command}, ctx.Workdir, ctx.Env, int(size.Rows), int(size.Cols))
			if err != nil {
				return "", err
			}
			fmt.Fprintln(ui.output, "Live command started. Exit or detach to return to review.")
			rc, err = p.Interact(input, ui.output)
			stdout = append([]byte(nil), p.Buffer...)
			p.Terminate()
			if err != nil {
				runError = err.Error()
			}
			record["type"] = "live_terminal"
		} else {
			r := ctx.Run([]string{"sh", "-lc", command}, nil, 120*time.Second, "")
			rc, timedOut, runError, stdout, stderr = r.ReturnCode, r.TimedOut, r.Error, r.Stdout, r.Stderr
			fmt.Fprintf(ui.output, "[exit %d]\n%s%s\n", rc, stdout, stderr)
		}
		content := fmt.Sprintf("$ %s\n\n[exit] %d\n[timeout] %v\n[runner error] %s\n\n[stdout]\n%s\n[stderr]\n%s", command, rc, timedOut, runError, stdout, stderr)
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			return "", err
		}
		record["command"], record["exit_code"] = command, rc
		record["timed_out"], record["runner_error"] = timedOut, runError
	}
	path, err := filepath.Rel(root, target)
	if err != nil {
		return "", err
	}
	sha, err := util.Sha256File(target)
	if err != nil {
		return "", err
	}
	record["path"], record["sha256"] = path, sha
	if err := s.AddEvidence(stage, record); err != nil {
		return "", err
	}
	return path, nil
}

func shellQuoteCLI(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

func expandEvidenceCommand(ctx *runner.Context, command string) string {
	command = strings.ReplaceAll(command, "{binary}", shellQuoteCLI(ctx.Binary))
	if strings.Contains(command, "{session}") {
		ctx.Sessions[ctx.ReviewSessionName()] = true
		command = strings.ReplaceAll(command, "{session}", ctx.ReviewSessionName())
	}
	return command
}
