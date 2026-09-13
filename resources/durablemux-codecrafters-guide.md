# Build Your Own Durable Terminal Multiplexer

## A CodeCrafters-Style Go Challenge

**Project name:** DurableMux  
**Binary name:** `dmux`  
**Primary platform:** Linux  
**Language:** Go  
**Method:** Manual implementation, black-box testing, specifications first  
**Core challenge:** 38 stages  
**Optional extensions:** 9 stages

---

## 1. What You Will Build

You will build a local terminal-session system that can:

- start an interactive shell or command inside a PTY;
- let a client detach without terminating the command;
- reconnect to the same session later;
- preserve sessions if the central coordinator restarts;
- support multiple attached clients safely;
- retain bounded scrollback output;
- handle terminal resizing, signals and process groups correctly;
- remain responsive when a client is slow;
- expose enough diagnostics to debug failures and measure performance.

The final core interface should resemble:

```text
dmux run -- <command> [arguments...]
dmux new <name> [-- <command> [arguments...]]
dmux attach <name>
dmux list
dmux inspect <name>
dmux logs <name> [--tail N]
dmux kill <name>
dmux daemon start
```

The precise internal architecture is yours to discover. The external behaviour is the contract.

---

## 2. Challenge Rules

1. **Do not copy an existing multiplexer.** Do not read the `tmux`, `screen`, `zellij`, or similar implementation source until you complete the core track.
2. **Use documentation, not generated implementation code.** Read manual pages, standards and Go package documentation.
3. **Implement one stage at a time.** Do not build future features before the current contract passes.
4. **Test through the public interface.** Prefer launching `dmux` as another process instead of testing private functions only.
5. **Commit every passing stage.** Use tags such as `stage-05-pty-shell`.
6. **Write down every changed assumption.** Systems knowledge grows when an observation disproves your mental model.
7. **Measure before optimising.** Performance claims require reproducible benchmarks.
8. **Document deliberate limitations.** A clearly bounded system is stronger than a vague, unfinished one.

---

## 3. Suggested Repository Documents

Create these before writing the product:

```text
docs/
├── glossary.md
├── learning-log.md
├── invariants.md
├── protocol.md
├── process-model.md
├── failure-model.md
├── security-model.md
├── benchmark-plan.md
└── decisions/
```

For every stage, add a short learning-log entry:

```text
Stage:
Initial assumption:
Experiment:
Observation:
Revised mental model:
Remaining question:
```

---

## 4. Passing a Stage

A stage passes only when:

- all stated acceptance tests pass;
- previous stages still pass;
- the race detector passes where applicable;
- terminal state is not left damaged after the test;
- the behaviour is documented if the operating system permits more than one valid result;
- you can explain why it works without referring to your implementation.

Each stage below contains:

- **Objective** — the capability to add;
- **Contract** — externally observable behaviour;
- **Acceptance tests** — black-box checks;
- **Study before implementation** — the minimum reading;
- **Questions to answer** — the mental model required to claim completion.

---

# Module A — Process and CLI Foundations

## Stage 1: A Stable Command-Line Contract

### Objective
Create a predictable CLI shell for the project.

### Contract

- `dmux version` prints one version line and exits successfully.
- `dmux help` displays available commands.
- An unknown command exits non-zero and writes a useful error to standard error.
- Diagnostic errors never contaminate command output on standard output.

### Acceptance tests

- Redirect standard output and standard error into different files and verify separation.
- Run an unknown command and inspect its exit status.
- Execute all commands from a directory other than the repository root.

### Study before implementation

- `os.Args`
- exit-status conventions
- standard input, output and error
- `getopt(3)` concepts, even if you use a Go parser

### Questions to answer

- Which output is machine-readable and which output is diagnostic?
- Which errors belong to the user, command, protocol or operating system?

---

## Stage 2: Execute a Command Without a Shell

### Objective
Run an external program using an argument array.

### Contract

```text
dmux run -- printf hello
```

must run the requested executable directly. Shell operators such as pipes, redirects and variable expansion must not be interpreted unless the user explicitly invokes a shell.

### Acceptance tests

- Run `printf` with arguments containing spaces.
- Run an executable by absolute path.
- Attempt to run a missing executable.
- Pass `*`, `$HOME`, `|` and `>` as literal arguments and verify that `dmux` does not interpret them.

### Study before implementation

- `execve(2)`
- `exec(3)` family concepts
- Go `os/exec`
- argument arrays versus shell command strings

### Questions to answer

- Why is implicit shell interpretation a security and correctness problem?
- At what point is executable lookup performed?

---

## Stage 3: Preserve Streams and Exit Status

### Objective
Make `dmux run` behave transparently for non-interactive commands.

### Contract

- Child standard output reaches caller standard output.
- Child standard error reaches caller standard error.
- Child standard input comes from caller standard input.
- `dmux` exits with the child’s normal exit status.
- Signal termination is represented consistently and documented.

### Acceptance tests

Run an explicitly invoked shell that:

- writes different strings to standard output and error;
- reads one line from standard input;
- exits with status 7.

Verify all four behaviours independently.

### Study before implementation

- `wait(2)`
- `waitpid(2)`
- process termination status
- Go `ProcessState`

### Questions to answer

- How is signal termination different from a normal non-zero exit?
- Who is responsible for reaping the child?

---

## Stage 4: Inspect the Process Tree

### Objective
Make process ownership observable before adding PTYs.

### Contract

While `dmux run -- sleep 30` is active, you must be able to identify:

- the invoking shell;
- the `dmux` process;
- the child command;
- PID, PPID, PGID, SID, TPGID and TTY for each relevant process.

This stage requires documentation and experiments, not a new user-facing feature.

### Acceptance tests

Capture and explain the output of:

```text
ps -o pid,ppid,pgid,sid,tpgid,stat,tty,cmd
```

### Study before implementation

- `credentials(7)`
- `/proc/<pid>/status`
- `/proc/<pid>/fd`
- `ps(1)` process fields

### Questions to answer

- Which process owns each file descriptor?
- Which processes belong to the same group and session?

---

## Module A Boss Test

Run a command that reads input, writes to both output streams, spawns a child and exits non-zero. Explain every observed process and descriptor relationship.

---

# Module B — Build an Interactive PTY Runner

## Stage 5: Allocate a PTY

### Objective
Run a command whose standard streams are connected to a pseudo-terminal slave.

### Contract

```text
dmux run -- tty
```

must report a PTY device rather than “not a tty”.

### Acceptance tests

- Run `tty` through `dmux`.
- Run a program that checks whether standard input and output are terminals.
- Compare its behaviour through a pipe and through `dmux`.

### Study before implementation

- `pty(7)`
- `pts(4)`
- `posix_openpt(3)`
- `grantpt(3)`
- `unlockpt(3)`
- `ptsname(3)`

### Questions to answer

- Which process owns the master?
- Which process sees the slave?
- Why are ordinary pipes insufficient for interactive software?

---

## Stage 6: Interactive Shell

### Objective
Start a usable interactive shell through the PTY.

### Contract

```text
dmux run -- bash
```

must display a prompt, accept commands and exit normally.

### Acceptance tests

Inside the shell:

- run `tty`;
- run `echo hello`;
- use command history;
- execute an external command;
- exit and verify the parent terminal remains usable.

### Study before implementation

- controlling terminals
- `setsid(2)`
- `TIOCSCTTY`
- descriptor duplication and closure

### Questions to answer

- How does the child acquire its controlling terminal?
- Why must unused copies of PTY descriptors be closed?

---

## Stage 7: Raw Client Terminal Mode

### Objective
Forward input immediately rather than line by line.

### Contract

Interactive full-screen programs and individual keystrokes must receive input without waiting for Enter.

### Acceptance tests

- Open `cat`, type characters and observe immediate behaviour.
- Open `vim` and enter/leave insert mode.
- Use arrow keys in the shell.
- Verify that typed characters are not double-echoed.

### Study before implementation

- `termios(3)`
- canonical mode
- echo flags
- `VMIN` and `VTIME`
- Go `x/term`

### Questions to answer

- Which terminal is put into raw mode: the real terminal or PTY slave?
- Which layer interprets control characters?

---

## Stage 8: Always Restore the User Terminal

### Objective
Prevent normal and catchable abnormal exits from leaving the terminal in raw mode.

### Contract

The original terminal configuration must be restored after:

- normal child exit;
- a command-start failure;
- an internal client error;
- a catchable termination signal.

`SIGKILL` cannot be caught; document the recovery command for that case.

### Acceptance tests

After each exit path:

- type normally;
- use Backspace;
- press Enter;
- inspect `stty -a` and compare essential settings.

### Study before implementation

- signal handling
- cleanup ordering
- Go deferred cleanup limitations
- `stty sane`

### Questions to answer

- Why can no in-process cleanup strategy handle `SIGKILL`?
- Which signals should trigger restoration before termination?

---

## Stage 9: Terminal Window Size

### Objective
Initialise the PTY with the client’s rows and columns.

### Contract

A command running inside `dmux` must initially observe the same terminal dimensions as the invoking terminal.

### Acceptance tests

Compare terminal dimensions inside and outside `dmux` using `stty size` or an equivalent utility.

### Study before implementation

- `TIOCGWINSZ`
- `TIOCSWINSZ`
- `ioctl_tty(2)`

### Questions to answer

- When should the initial size be set relative to command startup?
- What size should be used when no client terminal exists?

---

## Stage 10: Dynamic Resize

### Objective
Propagate terminal-resize events while a command is running.

### Contract

When the outer terminal changes size, an attached full-screen program must redraw to the new dimensions.

### Acceptance tests

- Run `watch 'stty size'` and resize repeatedly.
- Open `vim`, `less` or `htop` and verify redraw behaviour.
- Resize rapidly and confirm the client remains responsive.

### Study before implementation

- `SIGWINCH`
- foreground process groups
- resize-event coalescing

### Questions to answer

- Who receives `SIGWINCH` after the PTY size changes?
- What happens if resize notifications arrive faster than they can be processed?

---

## Stage 11: Foreground Signals

### Objective
Make terminal-generated interruption behave like a normal terminal.

### Contract

Inside an interactive shell:

- `Ctrl+C` interrupts the foreground command without terminating the shell;
- `Ctrl+Z` stops the foreground job when the shell supports job control;
- the shell remains interactive afterwards.

### Acceptance tests

- Run `sleep 30`, press `Ctrl+C`, then execute another command.
- Run `sleep 30`, press `Ctrl+Z`, inspect `jobs`, then use `fg` and `bg`.
- Inspect PID, PGID, SID and TPGID during each state.

### Study before implementation

- `signal(7)`
- `setpgid(2)`
- `tcsetpgrp(3)`
- `SIGINT`, `SIGTSTP`, `SIGCONT`, `SIGTTIN`, `SIGTTOU`
- POSIX job control

### Questions to answer

- Does `Ctrl+C` need to be manually forwarded in every design?
- Why are pipelines placed in process groups?

---

## Module B Boss Test

Run Bash through `dmux`, then successfully use:

- command history;
- `vim`;
- `less`;
- `Ctrl+C`;
- `Ctrl+Z`, `jobs`, `bg` and `fg`;
- repeated window resizing.

Exit and verify the outer terminal is healthy.

---

# Module C — Detachable Durable Sessions

## Stage 12: Define a Session Identity

### Objective
Introduce a durable session name and machine-readable identity.

### Contract

```text
dmux new development -- bash
```

creates a uniquely identifiable session. Duplicate active names must fail predictably.

### Acceptance tests

- Create two sessions with different names.
- Attempt to create a duplicate.
- Try empty, extremely long and path-like names.
- Verify that names cannot escape the runtime directory.

### Study before implementation

- XDG runtime-directory conventions
- filename safety
- atomic creation
- uniqueness and race conditions

### Questions to answer

- Is the user-facing name the authoritative identity?
- What identifier remains stable if a session is renamed later?

---

## Stage 13: A Session Outlives Its Creator

### Objective
Separate session ownership from the foreground CLI process.

### Contract

After `dmux new`, the session command must continue running when the creating client exits.

### Acceptance tests

- Create a session running a long command.
- Verify the original `dmux` command returns or detaches.
- Inspect the process tree.
- Confirm the command remains alive.

### Study before implementation

- daemonisation concepts
- parent death
- orphaned processes
- `SIGHUP`
- subreapers as optional Linux-specific reading

### Questions to answer

- Which process now owns the PTY master?
- Who reaps the shell when it exits?

---

## Stage 14: Attach Through a Unix Socket

### Objective
Connect a new client to a running session supervisor.

### Contract

```text
dmux attach development
```

must connect to the existing PTY session and allow interaction.

### Acceptance tests

- Create a session, leave it running and attach from another terminal.
- Execute commands after attachment.
- Disconnect the client unexpectedly and confirm the shell survives.

### Study before implementation

- `unix(7)`
- stream sockets
- `bind(2)`, `listen(2)`, `accept(2)`, `connect(2)`
- partial reads and writes

### Questions to answer

- What does socket EOF mean for the client and for the session?
- Who closes which connection after detachment?

---

## Stage 15: Explicit Detach

### Objective
Provide a keyboard sequence that detaches without sending the sequence to the child.

### Contract

Use a documented prefix sequence such as:

```text
Ctrl-a d
```

The attached client exits, but the shell and its foreground job remain alive.

### Acceptance tests

- Start `sleep 30` inside a session.
- Detach.
- Verify the process remains alive.
- Reattach before it finishes.
- Confirm ordinary `Ctrl+a` can still be sent through an escape mechanism.

### Study before implementation

- byte-stream state machines
- prefix-key ambiguity
- interrupted and partial input sequences

### Questions to answer

- How long may the parser wait after receiving the prefix?
- How does the user send a literal prefix byte?

---

## Stage 16: List Sessions

### Objective
Discover all currently live sessions.

### Contract

```text
dmux list
```

must show at least:

- stable ID;
- user-facing name;
- command;
- creation time;
- attached-client count;
- running or exited state.

A machine-readable format must also be available for testing.

### Acceptance tests

- List zero, one and several sessions.
- Include names containing safe spaces or punctuation if supported.
- Verify stable ordering or explicitly document that ordering is not guaranteed.

### Study before implementation

- runtime discovery
- directory iteration
- stale metadata
- machine-readable CLI output

### Questions to answer

- Is the filesystem listing authoritative, or must liveness be checked?
- How do you avoid reporting dead sessions as live?

---

## Stage 17: Terminate a Session

### Objective
Stop the shell and all processes belonging to the session.

### Contract

```text
dmux kill development
```

must terminate the complete managed process tree according to a documented graceful-shutdown policy.

### Acceptance tests

- Kill an idle shell.
- Kill a shell with a foreground `sleep`.
- Kill a shell that started background children.
- Verify no managed descendants remain.
- Repeat `kill` and confirm idempotent user-facing behaviour.

### Study before implementation

- process groups
- sessions
- `kill(2)` and `killpg(3)`
- graceful timeout and forced termination

### Questions to answer

- Why is killing only the immediate shell PID insufficient?
- Which descendants are legitimately outside your ownership boundary?

---

## Stage 18: Preserve Exit Records

### Objective
Retain a final session record after the command exits.

### Contract

`dmux inspect <name>` must report whether the command:

- is running;
- exited normally and with what code;
- was terminated by a signal;
- failed before execution.

### Acceptance tests

Create sessions that:

- exit 0;
- exit 7;
- terminate from a signal;
- reference a missing executable.

### Study before implementation

- wait status
- lifecycle-state machines
- terminal versus supervisor exit

### Questions to answer

- Which transitions are legal?
- Can a session move from exited back to running?

---

## Module C Boss Test

Start a long-running interactive session, detach, close the original terminal, attach from a new terminal, use job control, detach again, inspect it and terminate the complete session cleanly.

---

# Module D — Coordinator, Protocol and Recovery

## Stage 19: Introduce the Coordinator

### Objective
Route CLI discovery and control through a central `dmux` coordinator.

### Contract

Clients must no longer need to know individual supervisor socket paths. Session creation, listing, attachment lookup and termination begin through the coordinator.

### Acceptance tests

- Start the coordinator explicitly.
- Create and list sessions through it.
- Stop the coordinator while a session is running.
- Verify the session process still exists.

### Study before implementation

- service discovery
- control plane versus data plane
- ownership boundaries

### Questions to answer

- Which responsibilities must never be placed in the coordinator if sessions must survive its failure?
- Is attachment traffic proxied or handed off?

---

## Stage 20: Coordinator Auto-Start

### Objective
Allow normal CLI commands to start the coordinator safely when absent.

### Contract

Concurrent clients attempting the first command must result in one usable coordinator, not multiple conflicting instances.

### Acceptance tests

- Remove all coordinator runtime state.
- launch several `dmux list` commands concurrently.
- Verify only one active coordinator owns the control socket.

### Study before implementation

- lock files
- atomic socket binding
- startup races
- stale PID files

### Questions to answer

- Is a PID file proof that a process is alive?
- Which kernel operation can act as the final arbiter of ownership?

---

## Stage 21: A Framed Protocol

### Objective
Replace implicit stream assumptions with explicit messages.

### Contract

The protocol must support multiple message types over a byte stream and reject malformed or oversized frames safely.

At minimum, define messages for:

- hello;
- request;
- response;
- terminal input;
- terminal output;
- resize;
- detach;
- process exit;
- protocol error.

### Acceptance tests

- Send one frame in several writes.
- Send several frames in one write.
- Send a truncated frame.
- Advertise an excessive payload length.
- Send an unknown message type.

### Study before implementation

- message framing
- endianness
- length prefixes
- bounded allocation
- network byte streams

### Questions to answer

- Why must frame parsing be independent of socket read boundaries?
- What is the maximum permitted frame size and why?

---

## Stage 22: Protocol Version Negotiation

### Objective
Fail clearly when client and server protocol versions are incompatible.

### Contract

Connection startup must establish:

- protocol version;
- client role;
- requested capability set;
- server capability set.

### Acceptance tests

- Connect with the supported version.
- Connect with an older unsupported version.
- Connect with an unknown capability.
- Verify that errors are returned before session mutation.

### Study before implementation

- compatibility policy
- capability negotiation
- backwards-compatible field additions

### Questions to answer

- Which changes require a new major protocol version?
- Can unknown optional fields be safely ignored?

---

## Stage 23: Request Correlation and Errors

### Objective
Support concurrent control requests without confusing responses.

### Contract

Each request has an identity. Errors are structured and distinguish at least:

- invalid request;
- not found;
- already exists;
- permission denied;
- conflict;
- internal failure;
- incompatible version.

### Acceptance tests

- Send several requests without waiting for earlier responses.
- Return responses in a different order.
- Produce each public error class and inspect CLI exit codes.

### Study before implementation

- correlation IDs
- idempotency
- error taxonomies
- retry safety

### Questions to answer

- Which requests can be retried after a lost response?
- How will duplicate create requests be recognised?

---

## Stage 24: Recover After Coordinator Restart

### Objective
Rebuild coordinator state from live supervisors.

### Contract

After the coordinator is forcibly stopped and restarted:

- live sessions reappear in `dmux list`;
- their commands continue running;
- clients can attach again;
- stale records are not presented as healthy.

### Acceptance tests

1. Start several sessions.
2. Run a foreground job in each.
3. forcibly stop only the coordinator.
4. verify supervisor and child PIDs remain alive.
5. restart the coordinator.
6. list and reattach to every session.

### Study before implementation

- reconciliation
- heartbeats
- liveness probes
- authoritative state
- eventual consistency

### Questions to answer

- Who initiates recovery: coordinator, supervisor or both?
- How are duplicate registrations resolved?

---

## Stage 25: Stale Runtime-State Cleanup

### Objective
Detect sockets and metadata left after crashes or machine reboot.

### Contract

Startup must distinguish:

- a live coordinator;
- a live supervisor;
- a dead process with stale files;
- an inaccessible socket;
- corrupt metadata.

Cleanup must never terminate or overwrite an unrelated live process.

### Acceptance tests

- Leave a stale Unix socket.
- Leave metadata referencing a dead PID.
- Corrupt one metadata file.
- Run two cleanup attempts concurrently.

### Study before implementation

- PID reuse
- socket probing
- atomic rename
- runtime leases
- Linux pidfds as an optional extension

### Questions to answer

- Why is `kill(pid, 0)` alone not perfect identity verification?
- What stable evidence ties metadata to the intended process instance?

---

## Module D Boss Test

Run ten sessions, kill and restart the coordinator repeatedly, corrupt one dead session’s metadata, leave a stale socket and confirm that every live session remains attachable while dead state is classified safely.

---

# Module E — Multiple Clients, Scrollback and Backpressure

## Stage 26: Multiple Read-Only Observers

### Objective
Allow several clients to view the same session output.

### Contract

One session may have multiple attached observers. Every healthy observer receives new PTY output.

### Acceptance tests

- Attach from three terminals.
- Generate output continuously.
- Disconnect one observer abruptly.
- Verify other clients and the session continue unaffected.

### Study before implementation

- fan-out
- connection ownership
- independent client lifecycles

### Questions to answer

- Can one client’s socket error affect another client?
- Which goroutine or event loop owns the observer registry?

---

## Stage 27: Single Writer Lease

### Objective
Prevent concurrent clients from interleaving uncontrolled input.

### Contract

At most one client holds the input-writer lease. Other clients are read-only unless ownership is explicitly transferred.

### Acceptance tests

- Attach two clients.
- Attempt input from both.
- Transfer writer ownership.
- Disconnect the writer unexpectedly and verify a documented release or expiry policy.

### Study before implementation

- leases
- ownership transfer
- disconnect detection
- conflict handling

### Questions to answer

- Is writer ownership permanent, manually transferred or time-bound?
- What happens when the writer becomes unreachable during network or scheduler delay?

---

## Stage 28: Output Sequence Numbers

### Objective
Give each output segment a monotonic position.

### Contract

Clients can identify:

- the latest observed output sequence;
- whether any output was skipped;
- where replay should begin after reconnecting.

### Acceptance tests

- Generate many output events.
- Disconnect and reconnect from a known sequence.
- Force a replay gap and verify it is reported rather than silently hidden.

### Study before implementation

- monotonic counters
- wraparound policy
- replay cursors
- gap detection

### Questions to answer

- Does a sequence identify bytes, records or virtual-screen updates?
- What does sequence continuity mean across supervisor restart?

---

## Stage 29: Bounded In-Memory Scrollback

### Objective
Retain recent output without unbounded memory growth.

### Contract

Each session has a configurable byte-bounded history. Oldest output is discarded when capacity is exceeded.

### Acceptance tests

- Configure a very small buffer.
- Generate more output than capacity.
- Verify newest output remains available.
- Verify memory use stabilises rather than growing with total lifetime output.

### Study before implementation

- ring buffers
- byte versus line accounting
- amortised allocations
- bounded resource invariants

### Questions to answer

- Why is a line-bounded buffer difficult for arbitrary terminal output?
- What happens when one output record exceeds total capacity?

---

## Stage 30: Replay on Attach

### Objective
Show recent context immediately when a client attaches.

### Contract

```text
dmux attach development --replay 4096
```

replays up to the requested recent output before live streaming begins, without losing or duplicating the handover boundary.

### Acceptance tests

- Produce output before attachment.
- Attach while output continues rapidly.
- Verify the transition from replay to live output has a clear sequence boundary.

### Study before implementation

- snapshot-plus-stream race
- high-water marks
- atomic subscription registration

### Questions to answer

- How do you prevent output created during replay from being lost?
- Is duplicated output acceptable if clearly identified?

---

## Stage 31: Slow-Client Backpressure

### Objective
Ensure one slow client cannot freeze the PTY reader or other clients.

### Contract

Every client has bounded pending output. When a client exceeds its limit, apply a documented policy such as:

- disconnect;
- drop output and report a gap;
- switch to snapshot recovery.

The child process and healthy clients must remain responsive.

### Acceptance tests

- Attach one normal client and one client that stops reading.
- Generate sustained output.
- Verify bounded memory.
- Verify the normal client continues receiving data.
- Verify the slow client’s outcome matches policy.

### Study before implementation

- backpressure
- bounded queues
- head-of-line blocking
- non-blocking fan-out design

### Questions to answer

- What happens if the supervisor stops reading from the PTY?
- Which data may be dropped and which control messages must not be dropped?

---

## Stage 32: Session Logs

### Objective
Expose recent output without attaching interactively.

### Contract

```text
dmux logs development --tail 100
```

returns bounded recent output and terminates. Binary and invalid UTF-8 output must follow a documented policy.

### Acceptance tests

- Log plain lines.
- Log output without trailing newline.
- Log invalid UTF-8 bytes.
- Request more history than exists.
- Query an exited session.

### Study before implementation

- byte transparency
- text rendering boundaries
- stable machine-readable output

### Questions to answer

- Is the log raw PTY output or reconstructed text?
- Should terminal control sequences be preserved, stripped or optionally rendered?

---

## Module E Boss Test

Attach three clients, transfer writer ownership, stop one client from reading, generate sustained output larger than scrollback capacity, detach and reconnect from a known sequence. No healthy client or child process may stall, and any gap must be explicit.

---

# Module F — Security, Correctness and Operability

## Stage 33: Runtime Permissions

### Objective
Prevent other local users from discovering or controlling private sessions.

### Contract

- Runtime directories use restrictive permissions.
- Unix sockets are not world-accessible.
- Session files do not expose command environment or output unnecessarily.
- Unsafe pre-existing paths are rejected.

### Acceptance tests

- Inspect all modes with `stat`.
- Attempt to replace a runtime path with a symlink.
- Start with an insecure directory mode and verify safe refusal or repair policy.

### Study before implementation

- `umask(2)`
- Unix ownership and permissions
- symlink attacks
- TOCTOU races

### Questions to answer

- Which paths can an attacker pre-create?
- When must validation and opening be performed atomically?

---

## Stage 34: Local Peer Authentication

### Objective
Verify that a connecting local client is authorised.

### Contract

Do not trust a claimed user identity inside a protocol frame. Use operating-system-supported peer identity where possible and document platform limitations.

### Acceptance tests

- Connect as the owning user.
- Attempt unauthorised access from another user where your environment permits.
- Send a forged identity field and verify it has no authority.

### Study before implementation

- Unix-socket peer credentials
- `SO_PEERCRED`
- threat boundaries

### Questions to answer

- What is authenticated by filesystem permissions versus peer credentials?
- Which actions require additional authorisation?

---

## Stage 35: Input and Resource Limits

### Objective
Make every externally influenced allocation bounded.

### Contract

Define and enforce limits for:

- frame size;
- session-name length;
- number of sessions;
- clients per session;
- pending output per client;
- scrollback bytes;
- environment size;
- command argument count and total bytes.

### Acceptance tests

Attempt to exceed each limit and verify:

- predictable errors;
- no panic;
- no excessive allocation;
- continued service for valid clients.

### Study before implementation

- denial-of-service threats
- integer overflow
- allocation before validation

### Questions to answer

- Which limits are global, per user, per session or per connection?
- What happens when configuration lowers a limit below current usage?

---

## Stage 36: Structured Diagnostics

### Objective
Make failures diagnosable without corrupting the terminal stream.

### Contract

Structured diagnostics must include useful fields such as:

- component;
- session ID;
- client ID;
- operation;
- request ID;
- error class;
- latency;
- byte counts.

Sensitive terminal input and environment values must not be logged by default.

### Acceptance tests

- Trigger common errors and correlate logs across client, coordinator and supervisor.
- Verify terminal output remains byte-clean.
- Search logs for accidental command input or secrets.

### Study before implementation

- structured logging
- correlation IDs
- secret minimisation
- log levels

### Questions to answer

- Which component has enough context to classify each failure?
- Which fields have unbounded cardinality?

---

## Stage 37: Race, Fuzz and Stress Testing

### Objective
Prove correctness under concurrency and malformed input.

### Contract

The project must pass:

- unit and integration tests;
- Go race detection;
- protocol-decoder fuzzing;
- detach-prefix parser fuzzing;
- repeated attach/detach stress;
- concurrent list/create/kill operations.

### Acceptance tests

Run repeated scenarios until they exercise:

- client disconnect during output;
- kill during attach;
- coordinator restart during create;
- writer disconnect during handover;
- resize during detach;
- child exit during replay.

### Study before implementation

- Go race detector
- Go fuzzing
- deterministic test orchestration
- timeouts versus event-based synchronisation

### Questions to answer

- Which tests are flaky because the design is nondeterministic?
- Can clock sleeps be replaced with observable readiness conditions?

---

## Stage 38: Performance Baseline and Profile

### Objective
Produce defensible measurements and improve one verified bottleneck.

### Contract

Publish a benchmark report containing:

- machine and OS details;
- Go version;
- exact workload;
- number of repetitions;
- latency percentiles where relevant;
- CPU and memory profiles;
- before-and-after evidence for one optimisation.

Measure at least:

- attach latency;
- input-to-PTY forwarding latency;
- PTY-to-client forwarding latency;
- sustained throughput;
- memory per idle session;
- output fan-out to several clients;
- slow-client impact;
- scrollback replay speed;
- file descriptors and goroutines per session.

### Study before implementation

- Go benchmarks
- `pprof`
- `runtime/trace`
- `strace(1)`
- `perf(1)`
- coordinated omission and benchmark warm-up

### Questions to answer

- What user-visible problem does each metric represent?
- Did the optimisation move cost elsewhere?

---

## Core Challenge Final Boss

Demonstrate all of the following in one recorded session:

1. Start five durable interactive sessions.
2. Run full-screen applications and long-running jobs.
3. Attach multiple clients to one session.
4. transfer writer ownership.
5. make one client stop reading.
6. continue using a healthy client without stalling.
7. detach and reconnect with bounded replay.
8. kill the central coordinator forcibly.
9. restart it and recover all live sessions.
10. terminate one complete process group.
11. inspect an exited session’s status.
12. show structured diagnostics and performance measurements.
13. run the race detector and core stress suite.

At this point, you have built the portfolio project needed to address the primary terminal-multiplexer role gap.

---

# Optional Extension Track

These are valuable, but none is required before you apply for the role.

## Extension 39: ANSI/ECMA-48 Tokeniser

Parse control-sequence boundaries across arbitrary read chunks. Do not yet maintain a screen.

Required cases:

- ordinary printable bytes;
- C0 controls;
- ESC sequences;
- CSI;
- OSC termination;
- malformed or incomplete sequences;
- sequence split across several reads;
- several sequences in one read.

Study: ECMA-48 and xterm control-sequence documentation.

---

## Extension 40: Virtual Screen Grid

Maintain rows, columns, cursor position and basic character cells for a limited, documented sequence subset.

Required operations:

- printable characters;
- carriage return;
- line feed;
- backspace;
- cursor movement;
- erase line/display;
- basic scrolling.

Document unsupported Unicode-width behaviour initially.

---

## Extension 41: Alternate Screen and Attributes

Support:

- primary and alternate screen;
- save/restore cursor;
- basic colours and text attributes;
- resize of the virtual grid.

Use `vim`, `less` and `htop` as behavioural probes.

---

## Extension 42: Pane Model

Introduce a workspace containing several PTY-backed panes. Start with switching between panes before attempting simultaneous rendering.

Define:

- pane identity;
- active pane;
- pane lifecycle;
- input routing;
- size policy;
- failure isolation.

---

## Extension 43: Split-Pane Rendering

Render two or more virtual screens into one client terminal. Handle borders, focus, resize and minimum pane dimensions.

This stage requires a virtual screen; raw byte forwarding alone is insufficient.

---

## Extension 44: Agent-Friendly Control API

Add non-interactive operations for automation and AI agents:

- create session;
- send input;
- await output predicate;
- read output from sequence;
- inspect status;
- cancel command;
- enforce timeout;
- identify which agent owns the writer lease.

Every operation must have explicit idempotency and timeout semantics.

---

## Extension 45: Remote Transport

Permit a client and supervisor to communicate across hosts while preserving the same logical protocol.

You must address:

- encryption;
- authentication;
- network partitions;
- reconnect semantics;
- heartbeats;
- writer-lease expiry;
- output replay gaps;
- host identity.

Do not build custom cryptography.

---

## Extension 46: Reproducible Nix Packaging

Package the client, coordinator and supervisor with Nix. Add a development shell and reproducible build.

This extension directly helps bridge the separate infrastructure-role gap:

- Nix derivations;
- dependency pinning;
- Linux service configuration;
- immutable packaging assumptions.

---

## Extension 47: High-Performance Web Client

Build a browser client against the same session protocol.

Study and measure:

- keyboard-event fidelity;
- focus management;
- WebSocket framing;
- terminal rendering;
- backpressure;
- reconnect and replay;
- accessibility;
- input latency.

This extension directly helps bridge the separate web-role gap.

---

# Hint Ladder

Do not read a hint until you have:

1. reproduced the problem twice;
2. written down the expected kernel or protocol behaviour;
3. used `ps`, `/proc`, `strace`, logs or packet/frame dumps to locate the failing boundary;
4. read the relevant manual page again.

## Level 1 — Observation Hint

Ask: which component currently owns the resource that disappeared or blocked?

## Level 2 — Boundary Hint

Draw the exact boundary:

```text
real terminal ↔ client ↔ socket ↔ supervisor ↔ PTY master/slave ↔ shell ↔ foreground job
```

Mark where the observed byte, signal, close, resize or state transition should travel.

## Level 3 — Invariant Hint

Check these invariants:

- exactly one owner reaps each child;
- the session supervisor owns the PTY master;
- no slow client blocks the PTY reader;
- every externally influenced buffer is bounded;
- protocol parsing ignores socket read boundaries;
- terminal state is restored on every catchable exit path;
- coordinator death does not imply session death;
- stale metadata is never treated as proof of liveness;
- one writer lease exists at most;
- output loss is explicit, never silent.

## Level 4 — Tool Hint

Use one of:

- `ps -o pid,ppid,pgid,sid,tpgid,stat,tty,cmd`
- `pstree -p`
- `ls -l /proc/<pid>/fd`
- `cat /proc/<pid>/status`
- `readlink /proc/<pid>/fd/<n>`
- `stty -a`
- `strace -ff`
- `lsof`
- `ss -xlp`
- Go race detector
- `pprof`
- `runtime/trace`

## Level 5 — Specification Hint

Return to the relevant primary reference:

- processes: `fork(2)`, `execve(2)`, `wait(2)`;
- process groups and sessions: `setsid(2)`, `setpgid(2)`, `tcsetpgrp(3)`;
- terminal behaviour: `termios(3)`;
- PTYs: `pty(7)`, `pts(4)`, `ioctl_tty(2)`;
- signals: `signal(7)`;
- IPC: `unix(7)`, `socket(2)`;
- local security: `unix(7)`, permissions and peer credentials;
- terminal sequences: ECMA-48 and xterm control sequences.

---

# Recommended Reading Order by Module

## Before Module A

- `open(2)`
- `dup(2)`
- `fork(2)`
- `execve(2)`
- `wait(2)`
- `/proc` basics
- Go `os`, `io`, `os/exec`

## Before Module B

- `termios(3)`
- `pty(7)`
- `pts(4)`
- `setsid(2)`
- `setpgid(2)`
- `tcsetpgrp(3)`
- `ioctl_tty(2)`
- `signal(7)`
- POSIX terminal and job-control concepts

## Before Module C

- Unix daemon and process-lifetime concepts
- `SIGHUP`
- process-group signalling
- XDG runtime-directory conventions
- atomic file operations

## Before Module D

- `unix(7)`
- stream protocol framing
- request correlation
- reconciliation
- leases and heartbeats
- crash-consistent metadata

## Before Module E

- bounded queues
- ring buffers
- high-water marks
- backpressure
- single-writer ownership
- snapshot-plus-stream handover

## Before Module F

- Unix permissions and `umask`
- symlink and TOCTOU attacks
- peer credentials
- Go race detector
- Go fuzzing
- `pprof`, `runtime/trace`, `strace`, `perf`

---

# Review Questions Before You Apply

You should be able to answer all of these clearly:

1. Why does an interactive shell need a PTY rather than ordinary pipes?
2. What are the master and slave sides, and who owns each?
3. What is a controlling terminal?
4. What is the difference between a process, process group and session?
5. How does `Ctrl+C` reach the foreground job?
6. Why can a detached command survive its client?
7. What exact component must own the PTY for coordinator failure not to kill the session?
8. How do terminal resize events propagate?
9. Why can a socket `Read` return half a message or several messages?
10. How do you bound memory when output and clients are untrusted?
11. Why must a slow observer not block PTY reading?
12. How does a reconnecting client detect missing output?
13. How do you recover live sessions after coordinator restart?
14. Why are PID files and filesystem metadata insufficient proof of liveness?
15. How do you terminate a complete managed job rather than only its immediate shell?
16. How do you prevent other local users from attaching?
17. Which operations are idempotent and safe to retry?
18. Which state survives client, coordinator, supervisor and machine failure?
19. What performance measurements matter to an interactive terminal user?
20. What did profiling reveal that you would not have guessed?

---

# Portfolio Deliverables

Your public repository should ultimately contain:

- a focused README with a short demonstration;
- architecture and process-tree diagrams;
- a protocol specification;
- a lifecycle state machine;
- a failure matrix;
- a security and threat model;
- black-box integration tests;
- race, fuzz and stress results;
- a reproducible benchmark report;
- one incident-style write-up describing a difficult bug;
- one design decision you reversed after experimentation;
- a short comparison explaining what you intentionally did not copy from `tmux`.

The strongest story is not “I cloned tmux”. It is:

> I began with no direct PTY implementation experience, learned the Unix terminal and process model from primary documentation, built a durable Go session system stage by stage, tested its failure boundaries, and measured its performance.
