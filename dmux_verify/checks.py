from __future__ import annotations

import json
import os
import re
import shlex
import signal
import subprocess
import tempfile
import termios
import time
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from typing import Any, Callable

from .model import CheckResult
from .runner import CheckContext
from .util import get_any, parse_json_output, process_alive, wait_until

CheckFn = Callable[[CheckContext], list[CheckResult]]


def _single(ctx: CheckContext, name: str, fn: Callable[[], tuple[bool, str, bytes, bytes]]) -> CheckResult:
    started = time.monotonic()
    try:
        passed, detail, stdout, stderr = fn()
        return ctx.result(name, passed, detail, started, stdout, stderr)
    except Exception as exc:
        return ctx.result(name, False, f"{type(exc).__name__}: {exc}", started)


def stage_1(ctx: CheckContext) -> list[CheckResult]:
    out: list[CheckResult] = []
    def version():
        r = ctx.run_action("version")
        lines = [line for line in r.stdout.decode("utf-8", "replace").splitlines() if line.strip()]
        ok = r.returncode == 0 and len(lines) == 1
        return ok, f"exit={r.returncode}, non-empty lines={len(lines)}", r.stdout, r.stderr
    out.append(_single(ctx, "version prints exactly one line", version))

    def help_check():
        r = ctx.run_action("help")
        text = r.stdout.decode("utf-8", "replace").lower()
        ok = r.returncode == 0 and bool(text.strip()) and ("version" in text or "help" in text)
        return ok, f"exit={r.returncode}", r.stdout, r.stderr
    out.append(_single(ctx, "help is available", help_check))

    def unknown():
        r = ctx.run([str(ctx.binary), "__definitely_unknown_command__"])
        ok = r.returncode != 0 and not r.stdout.strip() and bool(r.stderr.strip())
        return ok, f"exit={r.returncode}; stdout must be empty and stderr non-empty", r.stdout, r.stderr
    out.append(_single(ctx, "unknown command separates diagnostics", unknown))

    def other_directory():
        with tempfile.TemporaryDirectory() as td:
            r = ctx.run(ctx.command("version"), cwd=Path(td))
        return r.returncode == 0, f"exit={r.returncode}", r.stdout, r.stderr
    out.append(_single(ctx, "works outside repository root", other_directory))
    return out


def stage_2(ctx: CheckContext) -> list[CheckResult]:
    out: list[CheckResult] = []
    printf = "/usr/bin/printf" if Path("/usr/bin/printf").exists() else "printf"

    def literals():
        args = ["hello world|$HOME>*"]
        r = ctx.run_action("run", extra=[printf, "%s", *args])
        expected = args[0].encode()
        return r.returncode == 0 and r.stdout == expected, f"exit={r.returncode}; expected literal shell characters", r.stdout, r.stderr
    out.append(_single(ctx, "argument array is not shell-interpreted", literals))

    def absolute():
        r = ctx.run_action("run", extra=[printf, "absolute-ok"])
        return r.returncode == 0 and r.stdout == b"absolute-ok", f"exit={r.returncode}", r.stdout, r.stderr
    out.append(_single(ctx, "absolute executable path works", absolute))

    def missing():
        r = ctx.run_action("run", extra=["/definitely/missing/dmux-verifier-executable"])
        return r.returncode != 0, f"exit={r.returncode}", r.stdout, r.stderr
    out.append(_single(ctx, "missing executable fails", missing))
    return out


def stage_3(ctx: CheckContext) -> list[CheckResult]:
    def streams():
        script = "read line; printf 'OUT:%s' \"$line\"; printf 'ERR:%s' \"$line\" >&2; exit 7"
        r = ctx.run_action("run", extra=["sh", "-c", script], input_data=b"hello\n")
        ok = r.returncode == 7 and r.stdout == b"OUT:hello" and r.stderr == b"ERR:hello"
        return ok, f"exit={r.returncode}; expected child exit 7 and transparent streams", r.stdout, r.stderr
    return [_single(ctx, "stdin/stdout/stderr and exit status are transparent", streams)]


def stage_5(ctx: CheckContext) -> list[CheckResult]:
    out: list[CheckResult] = []
    def tty_check():
        p = ctx.spawn_pty("run", extra=["tty"])
        try:
            deadline = time.monotonic() + ctx.timeout
            while p.poll() is None and time.monotonic() < deadline:
                p.read_some(0.1)
            rc = p.wait(1)
            data = bytes(p.buffer)
            text = data.decode("utf-8", "replace").strip()
            ok = rc == 0 and "not a tty" not in text and bool(re.search(r"/dev/(pts/\d+|tty\S*)", text))
            return ok, f"reported terminal={text!r}", data, b""
        finally:
            p.terminate()
    out.append(_single(ctx, "child is connected to a PTY", tty_check))

    def isatty_check():
        code = "import os; print(int(os.isatty(0)), int(os.isatty(1)), int(os.isatty(2)))"
        p = ctx.spawn_pty("run", extra=["python3", "-c", code])
        try:
            data = p.read_until(b"1 1 1", ctx.timeout)
            rc = p.wait(ctx.timeout)
            ok = rc == 0 and b"1 1 1" in data
            return ok, "stdin, stdout and stderr should all be terminal devices", data, b""
        finally:
            p.terminate()
    out.append(_single(ctx, "all standard streams are TTYs", isatty_check))
    return out


def stage_6(ctx: CheckContext) -> list[CheckResult]:
    def interactive():
        p = ctx.spawn_pty("run", extra=["bash", "--noprofile", "--norc"])
        try:
            p.write(b"echo __DMUX_STAGE6_OK__\nexit\n")
            data = p.read_until(b"__DMUX_STAGE6_OK__", ctx.timeout)
            rc = p.wait(ctx.timeout)
            return rc == 0, f"interactive shell marker observed; exit={rc}", data, b""
        finally:
            p.terminate()
    return [_single(ctx, "interactive shell accepts commands", interactive)]


def stage_7(ctx: CheckContext) -> list[CheckResult]:
    def immediate_key():
        code = (
            "import os,tty,termios; fd=0; old=termios.tcgetattr(fd); "
            "tty.setraw(fd); b=os.read(fd,1); os.write(1,b'__KEY__'+b); termios.tcsetattr(fd,termios.TCSANOW,old)"
        )
        p = ctx.spawn_pty("run", extra=["python3", "-c", code])
        try:
            time.sleep(0.2)
            p.write(b"Z")
            data = p.read_until(b"__KEY__Z", 1.5)
            rc = p.wait(ctx.timeout)
            return rc == 0, "single byte reached child without Enter", data, b""
        finally:
            p.terminate()
    return [_single(ctx, "client terminal is switched to immediate/raw input", immediate_key)]


def stage_8(ctx: CheckContext) -> list[CheckResult]:
    results: list[CheckResult] = []
    def normal_restore():
        p = ctx.spawn_pty("run", extra=["sh", "-c", "sleep 0.2"])
        rc = p.wait(ctx.timeout)
        after = p.attrs()
        mask = termios.ICANON | termios.ECHO
        ok = rc == 0 and (after[3] & mask) == mask
        data = bytes(p.buffer)
        p.close()
        return ok, f"exit={rc}; ICANON/ECHO restored", data, b""
    results.append(_single(ctx, "terminal state restored after normal exit", normal_restore))

    def failed_start_restore():
        p = ctx.spawn_pty("run", extra=["/definitely/missing/dmux-command"])
        rc = p.wait(ctx.timeout)
        after = p.attrs()
        mask = termios.ICANON | termios.ECHO
        ok = rc != 0 and (after[3] & mask) == mask
        data = bytes(p.buffer)
        p.close()
        return ok, f"exit={rc}; ICANON/ECHO restored", data, b""
    results.append(_single(ctx, "terminal state restored after command-start failure", failed_start_restore))
    return results


def stage_9(ctx: CheckContext) -> list[CheckResult]:
    def initial_size():
        p = ctx.spawn_pty("run", rows=37, cols=101, extra=["stty", "size"])
        try:
            data = p.read_until(b"37 101", ctx.timeout)
            rc = p.wait(ctx.timeout)
            return rc == 0, f"expected 37x101; exit={rc}", data, b""
        finally:
            p.terminate()
    return [_single(ctx, "initial terminal size is propagated", initial_size)]


def stage_10(ctx: CheckContext) -> list[CheckResult]:
    def resize():
        code = (
            "import fcntl,signal,struct,termios,time; "
            "size=lambda: struct.unpack('HHHH',fcntl.ioctl(0,termios.TIOCGWINSZ,struct.pack('HHHH',0,0,0,0)))[:2]; "
            "h=lambda a,b: print('__SIZE__%dx%d'%size(),flush=True); "
            "signal.signal(signal.SIGWINCH,h); print('__READY__',flush=True); "
            "time.sleep(10)"
        )
        p = ctx.spawn_pty("run", rows=24, cols=80, extra=["python3", "-c", code])
        try:
            p.read_until(b"__READY__", ctx.timeout)
            p.set_size(41, 109)
            data = p.read_until(b"__SIZE__41x109", 2.5)
            return True, "SIGWINCH and new dimensions reached child", data, b""
        finally:
            p.terminate()
    return [_single(ctx, "dynamic terminal resize is propagated", resize)]


def stage_11(ctx: CheckContext) -> list[CheckResult]:
    results: list[CheckResult] = []
    def ctrl_c():
        p = ctx.spawn_pty("run", extra=["bash", "--noprofile", "--norc"])
        try:
            p.write(b"sleep 30\n")
            time.sleep(0.3)
            p.write(b"\x03")
            p.write(b"echo __SHELL_ALIVE__\nexit\n")
            data = p.read_until(b"__SHELL_ALIVE__", 3)
            rc = p.wait(ctx.timeout)
            return rc == 0, "Ctrl+C interrupted foreground command and shell survived", data, b""
        finally:
            p.terminate()
    results.append(_single(ctx, "Ctrl+C preserves interactive shell", ctrl_c))

    def ctrl_z():
        p = ctx.spawn_pty("run", extra=["bash", "--noprofile", "--norc"])
        try:
            p.write(b"sleep 30\n")
            time.sleep(0.3)
            p.write(b"\x1a")
            time.sleep(0.2)
            p.write(b"jobs\n")
            data = p.read_until(b"Stopped", 3)
            p.write(b"kill %1\nexit\n")
            return True, "Ctrl+Z created a stopped job visible to the shell", data, b""
        finally:
            p.terminate()
    results.append(_single(ctx, "Ctrl+Z supports shell job control", ctrl_z))
    return results


def stage_12(ctx: CheckContext) -> list[CheckResult]:
    results: list[CheckResult] = []
    name = f"verify-{os.getpid()}-identity"
    ctx.sessions.add(name)

    def duplicate():
        argv = ctx.command("new", name=name) + ["sleep", "30"]
        creator = subprocess.Popen(argv, cwd=ctx.workdir, env=ctx.env, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        try:
            time.sleep(0.4)
            second = ctx.run_action("new", name=name, extra=["sleep", "30"], timeout=2)
            ok = second.returncode != 0
            return ok, f"duplicate exit={second.returncode}", second.stdout, second.stderr
        finally:
            creator.terminate()
            try:
                creator.wait(timeout=1)
            except subprocess.TimeoutExpired:
                creator.kill()
    results.append(_single(ctx, "duplicate active session name is rejected", duplicate))

    def unsafe_names():
        bad = ["", "../escape", "/absolute", "a/b", "x" * 300]
        details = []
        all_bad = True
        for value in bad:
            r = ctx.run_action("new", name=value, extra=["true"], timeout=2)
            details.append(f"{value[:20]!r}:{r.returncode}")
            all_bad = all_bad and r.returncode != 0
        return all_bad, "; ".join(details), b"", b""
    results.append(_single(ctx, "path-like and excessive names are rejected", unsafe_names))
    return results


def _write_pid_command(pidfile: Path, *, background: bool = False) -> list[str]:
    if background:
        script = f"echo $$ > {shlex.quote(str(pidfile))}; sleep 60 & echo $! > {shlex.quote(str(pidfile))}.child; wait"
    else:
        script = f"echo $$ > {shlex.quote(str(pidfile))}; sleep 60"
    return ["sh", "-c", script]


def stage_13(ctx: CheckContext) -> list[CheckResult]:
    def survives():
        name = f"verify-{os.getpid()}-durable"
        pidfile = Path(ctx.temp.name) / "session.pid"
        r = ctx.new_session(name, _write_pid_command(pidfile), timeout=3)
        ready = wait_until(pidfile.exists, 2)
        pid = int(pidfile.read_text().strip()) if ready else -1
        alive = ready and process_alive(pid)
        ok = r.returncode == 0 and alive
        return ok, f"new exit={r.returncode}; pid={pid}; alive={alive}", r.stdout, r.stderr
    return [_single(ctx, "session survives creating CLI", survives)]


def stage_14(ctx: CheckContext) -> list[CheckResult]:
    def attach_interact():
        name = f"verify-{os.getpid()}-attach"
        pidfile = Path(ctx.temp.name) / "attach.pid"
        created = ctx.new_session(name, ["sh", "-c", f"echo $$ > {shlex.quote(str(pidfile))}; exec sh"], timeout=3)
        if created.returncode != 0 or not wait_until(pidfile.exists, 2):
            return False, "failed to create attachable session", created.stdout, created.stderr
        shell_pid = int(pidfile.read_text().strip())
        p = ctx.spawn_pty("attach", name=name)
        try:
            p.write(b"echo __ATTACH_OK__\n")
            data = p.read_until(b"__ATTACH_OK__", 3)
            os.kill(p.pid, signal.SIGKILL)
            try:
                p.wait(1)
            except TimeoutError:
                pass
            alive = process_alive(shell_pid)
            return alive, f"interaction succeeded; shell alive after client SIGKILL={alive}", data, b""
        finally:
            p.terminate()
    return [_single(ctx, "attach supports interaction and client crash isolation", attach_interact)]


def stage_15(ctx: CheckContext) -> list[CheckResult]:
    def detach():
        name = f"verify-{os.getpid()}-detach"
        pidfile = Path(ctx.temp.name) / "detach.pid"
        created = ctx.new_session(name, ["sh", "-c", f"echo $$ > {shlex.quote(str(pidfile))}; exec sh"], timeout=3)
        if created.returncode != 0 or not wait_until(pidfile.exists, 2):
            return False, "failed to create session", created.stdout, created.stderr
        shell_pid = int(pidfile.read_text().strip())
        p = ctx.spawn_pty("attach", name=name)
        try:
            p.write(b"sleep 30\n")
            time.sleep(0.3)
            sequence = ctx.config.get("detach_sequence", "\u0001d").encode("latin1")
            p.write(sequence)
            rc = p.wait(3)
            alive = process_alive(shell_pid)
            return alive and rc == 0, f"attach exit={rc}; shell alive={alive}", bytes(p.buffer), b""
        finally:
            p.terminate()
    return [_single(ctx, "detach sequence leaves session alive", detach)]


def _sessions_from_json(ctx: CheckContext, payload: Any) -> list[dict[str, Any]]:
    if isinstance(payload, list):
        return [x for x in payload if isinstance(x, dict)]
    if isinstance(payload, dict):
        aliases = ctx.config["json_fields"].get("sessions_container", [])
        value = get_any(payload, aliases)
        if isinstance(value, list):
            return [x for x in value if isinstance(x, dict)]
    raise ValueError("list --json must return a JSON array or an object containing a sessions/items/data array")


def stage_16(ctx: CheckContext) -> list[CheckResult]:
    def list_json():
        names = [f"verify-{os.getpid()}-list-a", f"verify-{os.getpid()}-list-b"]
        for name in names:
            r = ctx.new_session(name, ["sleep", "60"], timeout=3)
            if r.returncode != 0:
                return False, f"could not create {name}", r.stdout, r.stderr
        r = ctx.run_action("list")
        payload = parse_json_output(r.stdout.decode("utf-8", "replace"))
        sessions = _sessions_from_json(ctx, payload)
        fields = ctx.config["json_fields"]
        found = {str(get_any(item, fields["name"])): item for item in sessions}
        required = ["id", "name", "command", "created_at", "attached_clients", "state"]
        missing_names = [name for name in names if name not in found]
        missing_fields = []
        for name in names:
            item = found.get(name, {})
            for field in required:
                if get_any(item, fields[field]) is None:
                    missing_fields.append(f"{name}.{field}")
        ok = r.returncode == 0 and not missing_names and not missing_fields
        return ok, f"missing sessions={missing_names}; missing fields={missing_fields}", r.stdout, r.stderr
    return [_single(ctx, "machine-readable session listing exposes required fields", list_json)]


def stage_17(ctx: CheckContext) -> list[CheckResult]:
    def kill_tree():
        name = f"verify-{os.getpid()}-kill"
        pidfile = Path(ctx.temp.name) / "kill.pid"
        created = ctx.new_session(name, _write_pid_command(pidfile, background=True), timeout=3)
        if created.returncode != 0 or not wait_until(lambda: pidfile.exists() and Path(str(pidfile)+".child").exists(), 2):
            return False, "failed to create process tree", created.stdout, created.stderr
        parent = int(pidfile.read_text().strip())
        child = int(Path(str(pidfile)+".child").read_text().strip())
        first = ctx.run_action("kill", name=name, timeout=4)
        dead = wait_until(lambda: not process_alive(parent) and not process_alive(child), 3)
        second = ctx.run_action("kill", name=name, timeout=2)
        ok = first.returncode == 0 and dead and second.returncode in (0, 1, 2)
        return ok, f"parent={parent}, child={child}, both dead={dead}, repeat exit={second.returncode}", first.stdout + second.stdout, first.stderr + second.stderr
    return [_single(ctx, "kill terminates the complete managed process tree", kill_tree)]


def _inspect(ctx: CheckContext, name: str) -> tuple[Any, bytes, bytes, int]:
    r = ctx.run_action("inspect", name=name)
    return parse_json_output(r.stdout.decode("utf-8", "replace")), r.stdout, r.stderr, r.returncode


def stage_18(ctx: CheckContext) -> list[CheckResult]:
    results: list[CheckResult] = []
    fields = ctx.config["json_fields"]
    def exit_code():
        name = f"verify-{os.getpid()}-exit7"
        created = ctx.new_session(name, ["sh", "-c", "exit 7"], timeout=3)
        if created.returncode != 0:
            return False, "session creation failed", created.stdout, created.stderr
        payload = None
        raw_out = raw_err = b""
        rc = 1
        deadline = time.monotonic() + 4
        while time.monotonic() < deadline:
            try:
                payload, raw_out, raw_err, rc = _inspect(ctx, name)
                if isinstance(payload, dict) and get_any(payload, fields["exit_code"]) is not None:
                    break
            except Exception:
                pass
            time.sleep(0.1)
        code = get_any(payload, fields["exit_code"]) if isinstance(payload, dict) else None
        state = get_any(payload, fields["state"]) if isinstance(payload, dict) else None
        ok = rc == 0 and int(code) == 7
        return ok, f"state={state!r}; exit_code={code!r}", raw_out, raw_err
    results.append(_single(ctx, "inspect preserves a non-zero normal exit record", exit_code))
    return results


def stage_20(ctx: CheckContext) -> list[CheckResult]:
    def concurrent_autostart():
        def one(_):
            return ctx.run_action("list", timeout=5)
        with ThreadPoolExecutor(max_workers=8) as pool:
            outputs = list(pool.map(one, range(8)))
        good = [o for o in outputs if o.returncode == 0]
        final = ctx.run_action("list", timeout=3)
        ok = len(good) == 8 and final.returncode == 0
        stderr = b"\n".join(o.stderr for o in outputs)
        return ok, f"successful concurrent clients={len(good)}/8; final exit={final.returncode}", final.stdout, stderr
    return [_single(ctx, "concurrent first clients converge on a usable coordinator", concurrent_autostart)]


def stage_32(ctx: CheckContext) -> list[CheckResult]:
    def logs():
        name = f"verify-{os.getpid()}-logs"
        marker = "__DMUX_LOG_MARKER__"
        created = ctx.new_session(name, ["sh", "-c", f"printf '{marker}'; sleep 2"], timeout=3)
        if created.returncode != 0:
            return False, "failed to create logging session", created.stdout, created.stderr
        time.sleep(0.3)
        r = ctx.run_action("logs", name=name, tail=100)
        ok = r.returncode == 0 and marker.encode() in r.stdout
        return ok, f"exit={r.returncode}; marker present={marker.encode() in r.stdout}", r.stdout, r.stderr
    return [_single(ctx, "logs returns recent output without attaching", logs)]


def stage_33(ctx: CheckContext) -> list[CheckResult]:
    def permissions():
        name = f"verify-{os.getpid()}-perm"
        created = ctx.new_session(name, ["sleep", "60"], timeout=3)
        if created.returncode != 0:
            return False, "failed to create session", created.stdout, created.stderr
        bad: list[str] = []
        for root, dirs, files in os.walk(ctx.runtime):
            for entry in [Path(root), *[Path(root)/d for d in dirs], *[Path(root)/f for f in files]]:
                try:
                    mode = entry.lstat().st_mode & 0o777
                except FileNotFoundError:
                    continue
                if mode & 0o077:
                    bad.append(f"{entry.relative_to(ctx.runtime)}:{oct(mode)}")
        return not bad, f"group/world-accessible paths={bad}", b"", b""
    return [_single(ctx, "runtime paths are private to the owner", permissions)]


def stage_37(ctx: CheckContext) -> list[CheckResult]:
    results: list[CheckResult] = []
    def go_test():
        if not (ctx.workdir / "go.mod").exists():
            return False, "go.mod not found in configured working_directory", b"", b""
        r = ctx.run(["go", "test", "./..."], timeout=120)
        return r.returncode == 0, f"exit={r.returncode}", r.stdout, r.stderr
    results.append(_single(ctx, "Go test suite passes", go_test))

    def race():
        if not (ctx.workdir / "go.mod").exists():
            return False, "go.mod not found in configured working_directory", b"", b""
        r = ctx.run(["go", "test", "-race", "./..."], timeout=180)
        return r.returncode == 0, f"exit={r.returncode}", r.stdout, r.stderr
    results.append(_single(ctx, "Go race detector passes", race))
    return results


REGISTRY: dict[str, CheckFn] = {
    name: obj for name, obj in globals().items()
    if name.startswith("stage_") and callable(obj)
}


def run_builtin(name: str, ctx: CheckContext) -> list[CheckResult]:
    try:
        fn = REGISTRY[name]
    except KeyError as exc:
        raise KeyError(f"Unknown built-in check group: {name}") from exc
    return fn(ctx)


def run_custom_checks(stage_number: int, ctx: CheckContext) -> list[CheckResult]:
    items = ctx.config.get("custom_checks", {}).get(str(stage_number), [])
    results: list[CheckResult] = []
    for index, item in enumerate(items, 1):
        if isinstance(item, list):
            name = f"custom check {index}"
            command = [str(x) for x in item]
            timeout = 120
        else:
            name = item.get("name", f"custom check {index}")
            command = [str(x) for x in item["command"]]
            timeout = float(item.get("timeout_seconds", 120))
        started = time.monotonic()
        r = ctx.run(command, timeout=timeout)
        results.append(ctx.result(name, r.returncode == 0, f"exit={r.returncode}; command={shlex.join(command)}", started, r.stdout, r.stderr))
    return results
