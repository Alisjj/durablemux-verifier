# DurableMux Progress Verifier

A local progress system for the **Build Your Own Durable Terminal Multiplexer** challenge.

It runs your `dmux` executable as a black box, stores stage history, locks later stages until prerequisites pass, collects evidence for design-heavy stages, and generates a Markdown progress report.

Single static binary with the guide + policies embedded; portable `darwin/arm64 + linux` (`doctor` no longer hard-fails off Linux; Linux-only checks gate at runtime).

## Requirements

- Go 1.21+
- Your `dmux` executable
- Bash, Python 3 and standard utilities (`bash`, `stty`)
- Go toolchain for the stage 37 checks

```bash
go install github.com/Alisjj/durablemux-verifier/cmd/dmux-verify@latest

# Or build from a source checkout:
make build          # produces ./dmux-verify (also syncs embedded resources)
go build -o dmux-verify ./cmd/dmux-verify
go install ./cmd/dmux-verify   # installs as dmux-verify from GOPATH/bin
```

## Updating

Once installed, check for and install the latest release with:

```bash
dmux-verify update --check
dmux-verify update
```

The updater downloads the release binary for the current operating system and architecture, verifies its GitHub SHA-256 digest, and atomically replaces the running executable. If the existing installation directory is not writable, rerun the command with the permissions used to install it.

Versions before the updater was introduced require a one-time update through Go:

```bash
go install github.com/Alisjj/durablemux-verifier/cmd/dmux-verify@latest
```

## Start

Copy this verifier anywhere, then initialise it inside your DurableMux repository:

```bash
/path/to/durablemux-verifier/dmux-verify --project /path/to/durablemux init --binary ./dmux
/path/to/durablemux-verifier/dmux-verify --project /path/to/durablemux doctor
```

The initialisation creates:

```text
.dmux-verifier/
├── config.json
├── progress.json
└── evidence/
```

Build your Go binary, then verify the next stage:

```bash
./dmux-verify --project /path/to/durablemux verify next
```

Or run a specific stage:

```bash
./dmux-verify --project /path/to/durablemux verify 5
```

## Main commands

Use the built-in help to see every command or the options and examples for one command:

```bash
dmux-verify help
dmux-verify help verify
dmux-verify verify --help
```

```bash
dmux-verify --project ./durablemux status
dmux-verify --project ./durablemux show 11
dmux-verify --project ./durablemux verify next
dmux-verify --project ./durablemux verify 11
dmux-verify --project ./durablemux review 11
dmux-verify --project ./durablemux report
```

Use `--force` only to investigate a locked stage without marking its prerequisites complete:

```bash
dmux-verify --project ./durablemux verify 14 --force
```

## Automated, hybrid and manual stages

The challenge contains behaviours that can be tested entirely through the CLI and others that depend on your internal protocol, failure model or performance methodology.

- **Auto:** the verifier runs all built-in black-box checks and marks the stage passed when they succeed.
- **Hybrid:** built-in/custom checks must pass and every guided review requirement must have a passing observation backed by recorded evidence.
- **Manual:** every contract/acceptance requirement and review question must be reviewed against evidence; configured custom checks must also pass.

Built-in automation is included for stages:

```text
1–3, 5–18, 20, 32, 33 and 37
```

Other stages remain fully trackable through evidence and custom checks rather than pretending that an implementation-specific property can be verified generically.

Fully automated stages are **1–3, 5, 9, 14 and 18**. Stages **6–8, 10–13, 15–17, 20, 32, 33 and 37** combine built-in probes with guided review of the remaining requirements. In particular, successful CLI calls alone do not prove coordinator ownership, full-screen redraw, fuzzing or stress coverage.

## Guided interactive review

```bash
dmux-verify --project ./durablemux review 10
```

The reviewer runs the stage's automated checks, then walks through the requirements that still need human observation. For each requirement you can:

- **`c`** — capture a command's output;
- **`l`** — launch a live terminal command, interact normally, resize the terminal, and return by exiting or detaching;
- **`a`** — copy an existing artefact;
- **`p` / `f` / `s`** — record pass, fail or skip, with an observation for pass/fail;
- **`q`** — save progress and quit; rerunning the command resumes the review.

Capture the experiment first, then record its observed result. A passing requirement must reference recorded evidence; existing evidence can be reused for several requirements covered by the same experiment. Pressing Enter keeps a saved result. Failed or skipped requirements prevent approval.

Captured commands share one private runtime throughout the review. Use `{binary}` for the configured executable and `{session}` for a unique review-session name, for example:

```text
{binary} run -- bash
{binary} new {session} -- bash
{binary} attach {session}
```

Live captures preserve terminal input, output, window resizing and the transcript. Command captures have a 120-second timeout. Sessions using `{session}` or suffixes such as `{session}-a` are tracked for cleanup; discovery only selects this review's generated names.

After all requirements pass, the reviewer asks for explicit approval. Complete the stage with:

```bash
dmux-verify --project ./durablemux verify 10
```

Reviews are bound to the target binary's SHA-256. Evidence is hashed and must still exist unchanged when approving or verifying. Rebuilds require review confirmation for the new binary. Old evidence without integrity metadata must be recorded again.

### Stage 4: process-tree review

```bash
dmux-verify --project ./durablemux review 4
```

At the result or evidence-selection prompt, press **`t`** to run the process-tree experiment. It starts an isolated invoking shell, runs your configured `run` command with `sleep 30`, and captures **PID, PPID, PGID, SID, TPGID, STAT, TTY and command** while the child is alive. It also records standard-descriptor targets using `/proc` on Linux or `lsof` on macOS. The experiment cleans up its processes and returns automatically; explain the captured relationships before marking each requirement passed. If `dmux` exec-replaces itself, the snapshot identifies that shared PID explicitly.

This works on Linux and macOS. The guide's Linux `ps` command uses `sid` and `cmd` fields that macOS does not support; the experiment obtains real session IDs with `getsid(PID)` rather than treating macOS `ps sess` pointers as session IDs. You can also import a plain-text snapshot with the same columns (`CMD` or `ARGS` may replace `COMMAND`). Failed commands, missing process fields, and snapshots without the invoking shell's live `sleep 30` process tree cannot support a passing process-tree requirement.

Stage 4 reviews now include the guide's previously omitted acceptance test. Earlier stage-4 passes need review and re-verification; saved observations and evidence remain available. Opening an unchanged approved review preserves its approval and pass; recording a changed result requires fresh approval.

## Evidence workflow

Record a command transcript on Linux while `dmux run -- sleep 30` is running in another terminal (the guided stage-4 experiment above handles this automatically on either platform):

```bash
dmux-verify --project ./durablemux evidence 4 \
  --command "ps -e -o pid,ppid,pgid,sid,tpgid,stat,tty,cmd" \
  --note "Captured while dmux run -- sleep 30 was active"
```

Or copy an existing artefact:

```bash
dmux-verify --project ./durablemux evidence 21 \
  --file ./test-results/protocol-framing.txt \
  --note "Fragmented, coalesced, oversized and unknown-frame tests"
```

Review the artefact against the individual requirements:

```bash
dmux-verify --project ./durablemux review 21

dmux-verify --project ./durablemux verify 21
```

Evidence is copied under `.dmux-verifier/evidence/stage-NN/` so the report remains self-contained.

`approve <stage> --note "..."` remains available after a complete guided review; evidence count alone cannot establish approval.

## Custom checks

Add implementation-specific commands to `.dmux-verifier/config.json`:

```json
{
  "custom_checks": {
    "21": [
      {
        "name": "protocol framing integration tests",
        "command": ["go", "test", "./internal/protocol", "-run", "TestFraming"],
        "timeout_seconds": 60
      }
    ],
    "31": [
      {
        "name": "slow client integration test",
        "command": ["go", "test", "./test/integration", "-run", "TestSlowClientDoesNotBlock"],
        "timeout_seconds": 120
      }
    ]
  }
}
```

Custom checks are executed without a shell. Each argument must be a separate JSON string.

## CLI adaptation

The default contract expects:

```text
dmux version
dmux help
dmux run -- <command> [arguments...]
dmux new <name> -- <command> [arguments...]
dmux attach <name>
dmux list --json
dmux inspect <name> --json
dmux logs <name> --tail <number>
dmux kill <name>
dmux daemon start
dmux daemon stop
```

If your syntax differs, edit the `commands` section in `config.json`. Placeholders supported by the built-in checks include:

```text
{name}
{tail}
```

For JSON output, the verifier accepts common aliases such as `id`/`session_id`, `state`/`status`, and `exit_code`/`exitCode`. These aliases are configurable under `json_fields`.

JSON output must contain exactly one complete JSON value and no trailing diagnostics. Session IDs must be non-empty strings or positive integers, attached-client counts must be non-negative integers, and creation times must be RFC3339 strings or positive Unix timestamps. Exit records must distinguish running, normal exit, signal termination and failed execution. A rejected creation still needs an inspectable failed-before-execution record.

Configure both `detach_sequence` and `literal_prefix_sequence` if your prefix/escape mechanism differs from the default `Ctrl+a d` / `Ctrl+a Ctrl+a`.

## Runtime isolation

Every verification run receives a new private temporary runtime through both:

```text
XDG_RUNTIME_DIR
DMUX_RUNTIME_DIR
```

Your implementation should honour at least one configured runtime variable. The verifier kills processes carrying that exact test-runtime environment after each run, preventing test daemons or session processes from being left behind.

This isolation is important: tests should never discover, attach to or terminate your normal sessions.

## Stage history and regressions

Every result records:

- check details and captured output;
- binary SHA-256;
- Git commit when available;
- evidence count;
- review approval;
- timestamp.

Normal verification reruns all earlier built-in and custom checks in fresh runtimes before testing the requested stage. A prior regression blocks completion and updates that stage's saved result. Earlier manual/hybrid reviews must also be valid and approved for the current binary. `--force` is an investigative run that skips prerequisite checks; it does not establish regression coverage.

Passes recorded before the hardened verification revision are displayed as **stale** and do not unlock later stages. History and evidence are retained; re-verify stages with the new checks and complete their guided reviews where required.

## Progress report

```bash
dmux-verify --project ./durablemux report
```

This writes:

```text
.dmux-verifier/report.md
```

It contains the stage table, results, per-requirement review observations and evidence references and can be included in your portfolio repository.

## Safety boundaries

The verifier intentionally does not:

- inspect your private Go functions;
- require a particular internal architecture;
- mark a manual stage complete without evidence and approval;
- silently invoke a shell for custom checks;
- use your normal runtime directory;
- treat a single successful run as proof of race freedom or performance.

## Run verifier self-tests

From the verifier directory:

```bash
go test ./...
```

## Included guide

The original challenge is included at:

```text
resources/durablemux-codecrafters-guide.md
```
