package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
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
	"github.com/Alisjj/durablemux-verifier/internal/updater"
	"github.com/Alisjj/durablemux-verifier/internal/util"
)

// Version is the dmux-verify CLI version.
const Version = "0.1.5"

type helpOption struct {
	name        string
	description string
}

type commandHelp struct {
	name        string
	usage       string
	summary     string
	description string
	options     []helpOption
	examples    []string
}

var globalHelpOptions = []helpOption{
	{"--project DIR", "DurableMux project directory (default: current directory)."},
	{"-V, --version", "Print the dmux-verify version and exit."},
	{"-h, --help", "Show help and exit."},
}

var commandHelps = []commandHelp{
	{
		name:        "init",
		usage:       "[--project DIR] init [--binary PATH] [--force]",
		summary:     "Initialise verifier state for a project",
		description: "Create .dmux-verifier configuration, progress state, and evidence storage. Existing progress is retained when --force replaces the configuration.",
		options: []helpOption{
			{"--binary PATH", "Path to the dmux executable, relative to the project (default: ./dmux)."},
			{"--force", "Replace an existing verifier configuration."},
		},
		examples: []string{
			"dmux-verify --project ./durablemux init --binary ./dmux",
		},
	},
	{
		name:        "doctor",
		usage:       "[--project DIR] doctor",
		summary:     "Check the environment and target binary",
		description: "Check required tools, platform information, and whether the configured dmux binary exists and is executable.",
		examples: []string{
			"dmux-verify --project ./durablemux doctor",
		},
	},
	{
		name:        "status",
		usage:       "[--project DIR] status [--json]",
		summary:     "Show progress across all challenge stages",
		description: "List every stage with its mode and current passed, ready, failed, or locked status.",
		options: []helpOption{
			{"--json", "Write machine-readable JSON instead of the progress table."},
		},
		examples: []string{
			"dmux-verify --project ./durablemux status",
			"dmux-verify --project ./durablemux status --json",
		},
	},
	{
		name:        "show",
		usage:       "[--project DIR] show <stage>",
		summary:     "Show the requirements for one stage",
		description: "Display a stage's objective, contract, acceptance tests, questions, evidence requirement, and current status.",
		examples: []string{
			"dmux-verify --project ./durablemux show 11",
		},
	},
	{
		name:        "verify",
		usage:       "[--project DIR] verify <stage|next> [--force]",
		summary:     "Run verification checks for a stage",
		description: "Run built-in and configured custom checks for a stage. Use next for the first incomplete stage. --force permits investigating a locked stage but does not complete its prerequisites.",
		options: []helpOption{
			{"--force", "Run a locked stage without marking its prerequisites complete."},
		},
		examples: []string{
			"dmux-verify --project ./durablemux verify next",
			"dmux-verify --project ./durablemux verify 14 --force",
		},
	},
	{
		name:        "evidence",
		usage:       "[--project DIR] evidence <stage> (--file PATH | --command COMMAND) [--note TEXT]",
		summary:     "Record evidence for a stage",
		description: "Copy an existing artefact or execute a shell command and save its transcript under .dmux-verifier/evidence.",
		options: []helpOption{
			{"--file PATH", "Copy a file into the stage's evidence directory."},
			{"--command COMMAND", "Run a shell command in the project and record its output."},
			{"--note TEXT", "Attach a note describing the evidence."},
		},
		examples: []string{
			"dmux-verify --project ./durablemux evidence 21 --file ./test-results/framing.txt --note \"Protocol tests\"",
			"dmux-verify --project ./durablemux evidence 4 --command \"ps -ef\" --note \"Process snapshot\"",
		},
	},
	{
		name:        "approve",
		usage:       "[--project DIR] approve <stage> --note TEXT",
		summary:     "Approve manual evidence for a stage",
		description: "Record manual review approval after the stage's minimum evidence requirement has been met.",
		options: []helpOption{
			{"--note TEXT", "Required explanation of what was reviewed and approved."},
		},
		examples: []string{
			"dmux-verify --project ./durablemux approve 21 --note \"Framing acceptance cases pass\"",
		},
	},
	{
		name:        "report",
		usage:       "[--project DIR] report [--output PATH]",
		summary:     "Generate a Markdown progress report",
		description: "Write a self-contained report of stage status, results, and evidence references.",
		options: []helpOption{
			{"--output PATH", "Output path, relative to the project (default: .dmux-verifier/report.md)."},
		},
		examples: []string{
			"dmux-verify --project ./durablemux report",
			"dmux-verify --project ./durablemux report --output ./progress.md",
		},
	},
	{
		name:        "reset",
		usage:       "[--project DIR] reset --stage N",
		summary:     "Reset saved state for one stage",
		description: "Remove a stage's saved results, evidence references, and approval. Copied evidence files are not deleted.",
		options: []helpOption{
			{"--stage N", "Stage number to reset (required)."},
		},
		examples: []string{
			"dmux-verify --project ./durablemux reset --stage 11",
		},
	},
	{
		name:        "update",
		usage:       "update [--check] [--force]",
		summary:     "Update dmux-verify to the latest release",
		description: "Check GitHub for the latest stable release and securely replace this executable with the matching platform binary. The download is verified against its SHA-256 release digest.",
		options: []helpOption{
			{"--check", "Check for an update without installing it."},
			{"--force", "Reinstall the latest release even when already up to date."},
		},
		examples: []string{
			"dmux-verify update --check",
			"dmux-verify update",
		},
	},
	{
		name:        "help",
		usage:       "help [command]",
		summary:     "Show help for the CLI or a command",
		description: "Show the full command overview or detailed help for one command.",
		examples: []string{
			"dmux-verify help",
			"dmux-verify help verify",
		},
	},
}

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
		usage(os.Stderr)
		return 2
	}
	cmd, args := rest[0], rest[1:]
	if cmd == "--help" || cmd == "-h" {
		usage(os.Stdout)
		return 0
	}
	if cmd == "help" {
		return cmdHelp(args)
	}
	if commandHelpFor(cmd) != nil && helpRequested(args) {
		writeCommandHelp(os.Stdout, commandHelpFor(cmd))
		return 0
	}
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
	case "update":
		return cmdUpdate(args)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		fmt.Fprintln(os.Stderr, "Run 'dmux-verify help' to see available commands.")
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "dmux-verify — progress verifier for the DurableMux challenge")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  dmux-verify [global options] <command> [arguments]")
	fmt.Fprintln(w, "  dmux-verify help [command]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	for _, command := range commandHelps {
		fmt.Fprintf(w, "  %-10s %s\n", command.name, command.summary)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Global options:")
	writeHelpOptions(w, globalHelpOptions)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  dmux-verify --project ./durablemux init --binary ./dmux")
	fmt.Fprintln(w, "  dmux-verify --project ./durablemux verify next")
	fmt.Fprintln(w, "  dmux-verify --project ./durablemux status")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'dmux-verify help <command>' for detailed command help.")
}

func cmdHelp(args []string) int {
	if len(args) == 0 {
		usage(os.Stdout)
		return 0
	}
	if len(args) > 1 {
		fmt.Fprintln(os.Stderr, "usage: dmux-verify help [command]")
		return 2
	}
	if args[0] == "--help" || args[0] == "-h" {
		writeCommandHelp(os.Stdout, commandHelpFor("help"))
		return 0
	}
	command := commandHelpFor(args[0])
	if command == nil {
		fmt.Fprintf(os.Stderr, "unknown help topic: %s\n", args[0])
		fmt.Fprintln(os.Stderr, "Run 'dmux-verify help' to see available commands.")
		return 2
	}
	writeCommandHelp(os.Stdout, command)
	return 0
}

func commandHelpFor(name string) *commandHelp {
	for i := range commandHelps {
		if commandHelps[i].name == name {
			return &commandHelps[i]
		}
	}
	return nil
}

func helpRequested(args []string) bool {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			return true
		}
	}
	return false
}

func writeCommandHelp(w io.Writer, command *commandHelp) {
	fmt.Fprintf(w, "dmux-verify %s — %s\n", command.name, command.summary)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintf(w, "  dmux-verify %s\n", command.usage)
	fmt.Fprintln(w)
	fmt.Fprintln(w, command.description)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Options:")
	writeHelpOptions(w, command.options)
	writeHelpOptions(w, []helpOption{{"-h, --help", "Show help for this command."}})
	if command.name != "help" && command.name != "update" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Global option:")
		writeHelpOptions(w, []helpOption{{"--project DIR", "DurableMux project directory (default: current directory)."}})
	}
	if len(command.examples) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Examples:")
		for _, example := range command.examples {
			fmt.Fprintf(w, "  %s\n", example)
		}
	}
}

func writeHelpOptions(w io.Writer, options []helpOption) {
	for _, option := range options {
		fmt.Fprintf(w, "  %-20s %s\n", option.name, option.description)
	}
}

func newCommandFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		if command := commandHelpFor(name); command != nil {
			writeCommandHelp(os.Stderr, command)
		}
	}
	return fs
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
	fs := newCommandFlagSet("init")
	binary := fs.String("binary", "./dmux", "path to the dmux executable")
	force := fs.Bool("force", false, "replace an existing verifier configuration")
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
	fs := newCommandFlagSet("status")
	asJSON := fs.Bool("json", false, "write machine-readable JSON")
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
	fs := newCommandFlagSet("evidence")
	fileFlag := fs.String("file", "", "copy a file as evidence")
	cmdFlag := fs.String("command", "", "run a command and record its transcript")
	note := fs.String("note", "", "describe the evidence")
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
	fs := newCommandFlagSet("approve")
	note := fs.String("note", "", "explain what was reviewed and approved")
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
	fs := newCommandFlagSet("report")
	output := fs.String("output", "", "output path relative to the project")
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
	fs := newCommandFlagSet("reset")
	stageFlag := fs.Int("stage", 0, "stage number to reset")
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

func cmdUpdate(args []string) int {
	fs := newCommandFlagSet("update")
	checkOnly := fs.Bool("check", false, "check for an update without installing it")
	force := fs.Bool("force", false, "reinstall the latest release")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "unexpected argument: %s\n", fs.Arg(0))
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client := updater.NewClient()
	latest, err := client.Latest(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Update check failed: %v\n", err)
		return 1
	}
	comparison, err := updater.CompareVersions(Version, latest.Version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Update check failed: %v\n", err)
		return 1
	}
	if comparison >= 0 && !*force {
		if comparison == 0 {
			fmt.Printf("dmux-verify %s is up to date.\n", Version)
		} else {
			fmt.Printf("dmux-verify %s is newer than the latest release (%s).\n", Version, latest.Version)
		}
		return 0
	}
	if *checkOnly {
		if comparison < 0 {
			fmt.Printf("Update available: %s -> %s\n", Version, latest.Version)
			fmt.Println("Run 'dmux-verify update' to install it.")
		} else {
			fmt.Printf("dmux-verify %s is up to date.\n", Version)
		}
		return 0
	}

	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Update failed: locate current executable: %v\n", err)
		return 1
	}
	fmt.Printf("Updating dmux-verify %s -> %s (%s)...\n", Version, latest.Version, latest.AssetName)
	if err := client.Install(ctx, latest, executable); err != nil {
		fmt.Fprintf(os.Stderr, "Update failed: %v\n", err)
		return 1
	}
	fmt.Printf("Updated dmux-verify to %s.\n", latest.Version)
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
