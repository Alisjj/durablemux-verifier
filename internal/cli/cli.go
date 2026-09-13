package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Alisjj/durablemux-verifier/internal/checks"
	"github.com/Alisjj/durablemux-verifier/internal/config"
	"github.com/Alisjj/durablemux-verifier/internal/guide"
	"github.com/Alisjj/durablemux-verifier/internal/model"
	"github.com/Alisjj/durablemux-verifier/internal/report"
	"github.com/Alisjj/durablemux-verifier/internal/runner"
	"github.com/Alisjj/durablemux-verifier/internal/store"
	"github.com/Alisjj/durablemux-verifier/internal/util"
)

// Version is the dmux-verify CLI version.
const Version = "0.1.2"

// Run dispatches argv (without program name) and returns exit code.
func Run(argv []string) int {
	project := "."
	rest := []string{}
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if a == "--project" && i+1 < len(argv) {
			project = argv[i+1]
			i++
		} else if strings.HasPrefix(a, "--project=") {
			project = strings.TrimPrefix(a, "--project=")
		} else if a == "--version" || a == "-V" {
			fmt.Println(Version)
			return 0
		} else {
			rest = append(rest, a)
		}
	}
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "usage: dmux-verify [--project DIR] <init|doctor|status|show|verify|evidence|approve|report|reset>")
		return 2
	}
	cmd, args := rest[0], rest[1:]
	switch cmd {
	case "init":
		return cmdInit(project, args)
	case "doctor":
		return cmdDoctor(project)
	case "status":
		return cmdStatus(project, args)
	case "show":
		return cmdShow(project, args)
	case "verify":
		return cmdVerify(project, args)
	case "evidence":
		return cmdEvidence(project, args)
	case "approve":
		return cmdApprove(project, args)
	case "report":
		return cmdReport(project, args)
	case "reset":
		return cmdReset(project, args)
	case "--help", "-h", "help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		return 2
	}
}

func usage() {
	fmt.Println("dmux-verify — progress verifier for the DurableMux challenge")
	fmt.Println("commands: init doctor status show verify evidence approve report reset")
}

func projectPaths(project string) (root, meta, cfg, prog string) {
	r, _ := filepath.Abs(project)
	m := filepath.Join(r, ".dmux-verifier")
	return r, m, filepath.Join(m, "config.json"), filepath.Join(m, "progress.json")
}

func loadAll(project string) (root, meta string, cfg map[string]any, stages map[int]*model.Stage, st *store.Store, err error) {
	root, meta, cfgPath, progPath := projectPaths(project)
	cfg, err = config.Load(cfgPath)
	if err != nil {
		err = fmt.Errorf("verifier is not initialised: %s does not exist (run init)", cfgPath)
		return
	}
	stages, err = guide.LoadEmbedded()
	if err != nil {
		return
	}
	st, err = store.New(progPath)
	return
}

func cmdInit(project string, args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	binary := fs.String("binary", "./dmux", "")
	force := fs.Bool("force", false, "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "unexpected argument: %s\n", fs.Arg(0))
		return 2
	}
	root, meta, cfgPath, progPath := projectPaths(project)
	if err := os.MkdirAll(root, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if _, err := os.Stat(cfgPath); err == nil && !*force {
		fmt.Fprintf(os.Stderr, "Already initialised: %s\n", cfgPath)
		return 2
	}
	if err := config.WriteDefault(cfgPath, *binary); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	st, err := store.New(progPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := st.Save(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_ = os.MkdirAll(filepath.Join(meta, "evidence"), 0o755)
	fmt.Printf("Initialised DurableMux verifier in %s\n", meta)
	fmt.Printf("Edit %s if your CLI differs from the default contract.\n", cfgPath)
	return 0
}

func cmdDoctor(project string) int {
	root, meta, cfg, _, _, err := loadAll(project)
	_ = meta
	if err != nil {
		fmt.Printf("FAIL: %v\n", err)
		return 1
	}
	bin, _ := cfg["binary"].(string)
	if !filepath.IsAbs(bin) {
		bin = filepath.Join(root, bin)
	}
	type check struct {
		ok   bool
		text string
	}
	_, binErr := os.Stat(bin)
	execOK := false
	if f, err := os.Stat(bin); err == nil && f.Mode()&0o111 != 0 {
		execOK = true
	}
	goOK := which("go")
	bashOK := which("bash")
	sttyOK := which("stty")
	list := []check{
		{true, fmt.Sprintf("platform is %s/%s (portable build; Linux-only checks gate at runtime)", runtime.GOOS, runtime.GOARCH)},
		{goOK, "Go is available (needed for stage 37)"},
		{bashOK, "bash is available"},
		{sttyOK, "stty is available"},
		{binErr == nil, fmt.Sprintf("binary exists at %s", bin)},
		{execOK, fmt.Sprintf("binary is executable: %s", bin)},
	}
	failed := false
	for _, c := range list {
		if c.ok {
			fmt.Printf("PASS  %s\n", c.text)
		} else {
			fmt.Printf("FAIL  %s\n", c.text)
			failed = true
		}
	}
	if failed {
		return 1
	}
	return 0
}

func which(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func firstMissingPrerequisite(n int, stages map[int]*model.Stage, st *store.Store) int {
	for _, prior := range sortedKeys(stages) {
		if prior >= n {
			break
		}
		if !st.IsPassed(prior) {
			return prior
		}
	}
	return 0
}

func stageStatus(n int, stages map[int]*model.Stage, st *store.Store) string {
	if firstMissingPrerequisite(n, stages, st) != 0 {
		return "locked"
	}
	if st.IsPassed(n) {
		return "passed"
	}
	item := st.Stage(n)
	if s, ok := item["status"].(string); ok && s != "" {
		return s
	}
	return "ready"
}

func cmdStatus(project string, args []string) int {
	_, _, _, stages, st, err := loadAll(project)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "unexpected argument: %s\n", fs.Arg(0))
		return 2
	}
	keys := sortedKeys(stages)
	if *asJSON {
		type row struct {
			Stage    int    `json:"stage"`
			Title    string `json:"title"`
			Mode     string `json:"mode"`
			Status   string `json:"status"`
			Evidence int    `json:"evidence"`
		}
		var payload []row
		for _, n := range keys {
			payload = append(payload, row{n, stages[n].Title, stages[n].Mode, stageStatus(n, stages, st), st.EvidenceCount(n)})
		}
		data, _ := json.MarshalIndent(payload, "", "  ")
		fmt.Println(string(data))
		return 0
	}
	passed := 0
	for _, n := range keys {
		if stageStatus(n, stages, st) == "passed" {
			passed++
		}
	}
	fmt.Printf("DurableMux progress: %d/%d stages passed\n\n", passed, len(stages))
	markers := map[string]string{"passed": "✓", "ready": "→", "failed": "✗", "locked": "·"}
	for _, n := range keys {
		status := stageStatus(n, stages, st)
		m, ok := markers[status]
		if !ok {
			m = "·"
		}
		fmt.Printf("%s %02d  %-7s  [%-6s]  %s\n", m, n, status, stages[n].Mode, stages[n].Title)
	}
	return 0
}

func cmdShow(project string, args []string) int {
	_, _, _, stages, st, err := loadAll(project)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: show <stage>")
		return 2
	}
	n, err := strconv.Atoi(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid stage")
		return 2
	}
	stage, ok := stages[n]
	if !ok {
		fmt.Fprintf(os.Stderr, "Unknown stage: %d\n", n)
		return 2
	}
	fmt.Printf("Stage %d: %s\n", stage.Number, stage.Title)
	fmt.Printf("%s | mode: %s | status: %s\n", stage.Module, stage.Mode, stageStatus(stage.Number, stages, st))
	fmt.Printf("\nObjective\n%s\n", stage.Objective)
	fmt.Printf("\nContract\n%s\n", stage.Contract)
	if len(stage.AcceptanceTests) > 0 {
		fmt.Println("\nAcceptance tests")
		for _, item := range stage.AcceptanceTests {
			fmt.Printf("- %s\n", item)
		}
	}
	if len(stage.Questions) > 0 {
		fmt.Println("\nQuestions to answer")
		for _, item := range stage.Questions {
			fmt.Printf("- %s\n", item)
		}
	}
	fmt.Printf("\nRequired evidence: %d; currently recorded: %d\n", stage.MinimumEvidence, st.EvidenceCount(stage.Number))
	return 0
}

func resolveStage(value string, stages map[int]*model.Stage, st *store.Store) (int, error) {
	if value == "next" {
		for _, n := range sortedKeys(stages) {
			if !st.IsPassed(n) {
				return n, nil
			}
		}
		return 0, fmt.Errorf("all core stages are already passed")
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("unknown stage: %s", value)
	}
	if _, ok := stages[n]; !ok {
		return 0, fmt.Errorf("unknown stage: %d", n)
	}
	return n, nil
}

func cmdVerify(project string, args []string) int {
	force := false
	stageArg := ""
	for _, a := range args {
		if a == "--force" {
			force = true
		} else if strings.HasPrefix(a, "-") {
			fmt.Fprintf(os.Stderr, "unknown option: %s\n", a)
			return 2
		} else if stageArg == "" {
			stageArg = a
		} else {
			fmt.Fprintf(os.Stderr, "unexpected argument: %s\n", a)
			return 2
		}
	}
	if stageArg == "" {
		fmt.Fprintln(os.Stderr, "usage: verify <stage|next> [--force]")
		return 2
	}
	root, _, cfg, stages, st, err := loadAll(project)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	number, err := resolveStage(stageArg, stages, st)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	stage := stages[number]
	if missing := firstMissingPrerequisite(number, stages, st); missing != 0 && !force {
		fmt.Fprintf(os.Stderr, "Stage %d is locked. Pass stage %d first, or use --force for investigation.\n", number, missing)
		return 2
	}
	bin, _ := cfg["binary"].(string)
	binAbs := bin
	if !filepath.IsAbs(binAbs) {
		binAbs = filepath.Join(root, bin)
	}
	if _, err := os.Stat(binAbs); err != nil {
		fmt.Fprintf(os.Stderr, "Binary not found: %s\n", binAbs)
		return 2
	}
	fmt.Printf("Verifying stage %d: %s [%s]\n", number, stage.Title, stage.Mode)
	ctx, err := runner.New(root, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer ctx.Close()
	var results []model.CheckResult
	for _, group := range stage.Checks {
		rs, err := checks.RunBuiltin(group, ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		results = append(results, rs...)
	}
	results = append(results, checks.RunCustomChecks(number, ctx)...)
	for _, r := range results {
		if r.Passed {
			fmt.Printf("PASS  %s\n", r.Name)
		} else {
			fmt.Printf("FAIL  %s\n", r.Name)
		}
		if r.Detail != "" {
			fmt.Printf("      %s\n", r.Detail)
		}
		if !r.Passed && strings.TrimSpace(r.Stderr) != "" {
			fmt.Printf("      stderr: %s\n", truncate(strings.ReplaceAll(strings.TrimSpace(r.Stderr), "\n", " | "), 500))
		}
	}
	checksPassed := true
	for _, r := range results {
		if !r.Passed {
			checksPassed = false
		}
	}
	evidenceOK := st.EvidenceCount(number) >= stage.MinimumEvidence
	approvalOK := stage.Mode == "auto" || st.Approved(number)
	if stage.Mode == "manual" || stage.Mode == "hybrid" {
		fmt.Printf("%s  evidence %d/%d\n", passNeed(evidenceOK), st.EvidenceCount(number), stage.MinimumEvidence)
		fmt.Printf("%s  manual review approval\n", passNeed(approvalOK))
	}
	passed := checksPassed && evidenceOK && approvalOK
	checkDicts := []any{}
	for _, r := range results {
		checkDicts = append(checkDicts, map[string]any{
			"name": r.Name, "passed": r.Passed, "detail": r.Detail,
			"duration_seconds": math.Round(r.DurationSeconds*10000) / 10000,
			"stdout":           r.Stdout, "stderr": r.Stderr,
		})
	}
	sha, _ := util.Sha256File(binAbs)
	payload := map[string]any{
		"at": store.UTCNow(), "passed": passed, "stage": number, "mode": stage.Mode,
		"checks": checkDicts, "evidence_count": st.EvidenceCount(number),
		"manual_approved": st.Approved(number), "git_commit": gitOrNil(root), "binary_sha256": sha,
	}
	if err := st.RecordRun(number, payload); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if passed {
		fmt.Printf("\nStage %d: PASSED\n", number)
	} else {
		fmt.Printf("\nStage %d: NOT PASSED\n", number)
	}
	if (stage.Mode == "manual" || stage.Mode == "hybrid") && !approvalOK {
		fmt.Printf("After reviewing the evidence, run: dmux-verify approve %d --note \"...\"\n", number)
	}
	if passed {
		return 0
	}
	return 1
}

func gitOrNil(root string) any {
	if c := util.GitCommit(root); c != "" {
		return c
	}
	return nil
}

func cmdEvidence(project string, args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: evidence <stage> --file X | --command Y [--note Z]")
		return 2
	}
	n, err := strconv.Atoi(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid stage")
		return 2
	}
	root, meta, _, stages, st, err := loadAll(project)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if _, ok := stages[n]; !ok {
		fmt.Fprintf(os.Stderr, "Unknown stage: %d\n", n)
		return 2
	}
	fs := flag.NewFlagSet("evidence", flag.ContinueOnError)
	fileFlag := fs.String("file", "", "")
	cmdFlag := fs.String("command", "", "")
	note := fs.String("note", "", "")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "unexpected argument: %s\n", fs.Arg(0))
		return 2
	}
	evDir := filepath.Join(meta, "evidence", fmt.Sprintf("stage-%02d", n))
	if err := os.MkdirAll(evDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	record := map[string]any{"at": store.UTCNow(), "note": *note}
	if *fileFlag != "" {
		src := *fileFlag
		if !filepath.IsAbs(src) {
			abs, _ := filepath.Abs(src)
			src = abs
		}
		if _, err := os.Stat(src); err != nil {
			fmt.Fprintf(os.Stderr, "Evidence file not found: %s\n", src)
			return 2
		}
		target := filepath.Join(evDir, stamp+"-"+filepath.Base(src))
		if err := copyFile(src, target); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		rel, _ := filepath.Rel(root, target)
		record["type"] = "file"
		record["path"] = rel
	} else if *cmdFlag != "" {
		c3 := exec.Command("sh", "-lc", *cmdFlag)
		c3.Dir = root
		soBuf, seBuf := &strings.Builder{}, &strings.Builder{}
		c3.Stdout = soBuf
		c3.Stderr = seBuf
		rc := 0
		if err := c3.Run(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				rc = ee.ExitCode()
			} else {
				rc = 1
			}
		}
		target := filepath.Join(evDir, stamp+"-command.txt")
		content := fmt.Sprintf("$ %s\n\n[exit] %d\n\n[stdout]\n%s\n[stderr]\n%s", *cmdFlag, rc, soBuf.String(), seBuf.String())
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		rel, _ := filepath.Rel(root, target)
		record["type"] = "command"
		record["command"] = *cmdFlag
		record["exit_code"] = rc
		record["path"] = rel
	} else {
		fmt.Fprintln(os.Stderr, "Provide --file or --command")
		return 2
	}
	if err := st.AddEvidence(n, record); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Recorded evidence for stage %d: %v\n", n, record["path"])
	return 0
}

func cmdApprove(project string, args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: approve <stage> --note \"...\"")
		return 2
	}
	n, err := strconv.Atoi(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid stage")
		return 2
	}
	fs := flag.NewFlagSet("approve", flag.ContinueOnError)
	note := fs.String("note", "", "")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "unexpected argument: %s\n", fs.Arg(0))
		return 2
	}
	if *note == "" {
		fmt.Fprintln(os.Stderr, "usage: approve <stage> --note \"...\"")
		return 2
	}
	_, _, _, stages, st, err := loadAll(project)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	stage, ok := stages[n]
	if !ok {
		fmt.Fprintf(os.Stderr, "Unknown stage: %d\n", n)
		return 2
	}
	if st.EvidenceCount(n) < stage.MinimumEvidence {
		fmt.Fprintf(os.Stderr, "Stage %d requires at least %d evidence item(s) before approval.\n", n, stage.MinimumEvidence)
		return 2
	}
	if err := st.Approve(n, *note); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Approved manual evidence for stage %d. Run verify %d to finalise it.\n", n, n)
	return 0
}

func cmdReport(project string, args []string) int {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	output := fs.String("output", "", "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "unexpected argument: %s\n", fs.Arg(0))
		return 2
	}
	root, meta, _, stages, st, err := loadAll(project)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	text := report.Markdown(stages, st)
	out := *output
	if out == "" {
		out = filepath.Join(meta, "report.md")
	}
	if !filepath.IsAbs(out) {
		out = filepath.Join(root, out)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.WriteFile(out, []byte(text+"\n"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Wrote report to %s\n", out)
	return 0
}

func cmdReset(project string, args []string) int {
	fs := flag.NewFlagSet("reset", flag.ContinueOnError)
	stageFlag := fs.Int("stage", 0, "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "unexpected argument: %s\n", fs.Arg(0))
		return 2
	}
	if *stageFlag == 0 {
		fmt.Fprintln(os.Stderr, "Specify --stage N")
		return 2
	}
	_, _, _, _, st, err := loadAll(project)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if m, ok := st.Data["stages"].(map[string]any); ok {
		delete(m, strconv.Itoa(*stageFlag))
	}
	if err := st.Save(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Reset stage %d\n", *stageFlag)
	return 0
}

func sortedKeys(stages map[int]*model.Stage) []int {
	keys := make([]int, 0, len(stages))
	for k := range stages {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func passNeed(ok bool) string {
	if ok {
		return "PASS"
	}
	return "NEED"
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}
