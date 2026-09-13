# DurableMux Progress Verifier

A local progress system for the **Build Your Own Durable Terminal Multiplexer** challenge.

It runs your `dmux` executable as a black box, stores stage history, locks later stages until prerequisites pass, collects evidence for design-heavy stages, and generates a Markdown progress report.

Single static binary with the guide + policies embedded; portable `darwin/arm64 + linux` (`doctor` no longer hard-fails off Linux; Linux-only checks gate at runtime).

## Requirements

- Go 1.21+
- Your `dmux` executable
- Bash and standard utilities (`bash`, `stty`)
- Go toolchain for the stage 37 checks

```bash
go install github.com/Alisjj/durablemux-verifier/cmd/dmux-verify@latest

# Or build from a source checkout:
make build          # produces ./dmux-verify (also syncs embedded resources)
go build -o dmux-verify ./cmd/dmux-verify
go install ./cmd/dmux-verify   # installs as dmux-verify from GOPATH/bin
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

```bash
dmux-verify --project ./durablemux status
dmux-verify --project ./durablemux show 11
dmux-verify --project ./durablemux verify next
dmux-verify --project ./durablemux verify 11
dmux-verify --project ./durablemux report
```

Use `--force` only to investigate a locked stage without marking its prerequisites complete:

```bash
dmux-verify --project ./durablemux verify 14 --force
```

## Automated, hybrid and manual stages

The challenge contains behaviours that can be tested entirely through the CLI and others that depend on your internal protocol, failure model or performance methodology.

- **Auto:** the verifier runs all built-in black-box checks and marks the stage passed when they succeed.
- **Hybrid:** built-in/custom checks must pass and you must record and approve evidence.
- **Manual:** you supply test evidence or add custom commands, then approve the review.

Built-in automation is included for stages:

```text
1–3, 5–18, 20, 32, 33 and 37
```

Other stages remain fully trackable through evidence and custom checks rather than pretending that an implementation-specific property can be verified generically.

## Evidence workflow

Record a command transcript:

```bash
dmux-verify --project ./durablemux evidence 4 \
  --command "ps -o pid,ppid,pgid,sid,tpgid,stat,tty,cmd" \
  --note "Captured while dmux run -- sleep 30 was active"
```

Or copy an existing artefact:

```bash
dmux-verify --project ./durablemux evidence 21 \
  --file ./test-results/protocol-framing.txt \
  --note "Fragmented, coalesced, oversized and unknown-frame tests"
```

After reviewing the artefact against the stage contract:

```bash
dmux-verify --project ./durablemux approve 21 \
  --note "All framing acceptance cases pass and allocations are bounded"

dmux-verify --project ./durablemux verify 21
```

Evidence is copied under `.dmux-verifier/evidence/stage-NN/` so the report remains self-contained.

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

Passing an earlier stage does not prove it still passes after later changes. Re-run prior stages before a milestone or release.

## Progress report

```bash
dmux-verify --project ./durablemux report
```

This writes:

```text
.dmux-verifier/report.md
```

It contains the stage table, results and evidence references and can be included in your portfolio repository.

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
