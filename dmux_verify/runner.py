from __future__ import annotations

import os
import shutil
import subprocess
import tempfile
import time
from contextlib import AbstractContextManager
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from .model import CheckResult
from .ptyproc import PTYProcess
from .util import cleanup_runtime_processes


@dataclass
class CommandOutput:
    argv: list[str]
    returncode: int
    stdout: bytes
    stderr: bytes
    duration: float


class CheckContext(AbstractContextManager):
    def __init__(self, project: Path, config: dict[str, Any]):
        self.project = project.resolve()
        self.config = config
        self.workdir = (self.project / config.get("working_directory", ".")).resolve()
        raw_binary = Path(config["binary"])
        self.binary = raw_binary if raw_binary.is_absolute() else (self.project / raw_binary).resolve()
        self.timeout = float(config.get("timeout_seconds", 8))
        self.temp = tempfile.TemporaryDirectory(prefix="dmux-verifier-")
        self.runtime = Path(self.temp.name) / "runtime"
        self.runtime.mkdir(mode=0o700)
        self.env = os.environ.copy()
        for key, value in config.get("runtime_environment", {}).items():
            self.env[key] = str(value).format(temp_runtime=self.runtime)
        self.sessions: set[str] = set()

    def __exit__(self, exc_type, exc, tb):
        for name in list(self.sessions):
            try:
                self.run_action("kill", name=name, timeout=2)
            except Exception:
                pass
        if "daemon_stop" in self.config.get("commands", {}):
            try:
                self.run_action("daemon_stop", timeout=2)
            except Exception:
                pass
        cleanup_runtime_processes(self.runtime)
        self.temp.cleanup()
        return False

    def command(self, action: str, **values: Any) -> list[str]:
        template = self.config["commands"].get(action)
        if template is None:
            raise KeyError(f"No command template configured for {action!r}")
        args = [str(part).format(**values) for part in template]
        return [str(self.binary), *args]

    def run_action(self, action: str, *, input_data: bytes | None = None, timeout: float | None = None, extra: list[str] | None = None, **values: Any) -> CommandOutput:
        argv = self.command(action, **values)
        if extra:
            argv.extend(extra)
        return self.run(argv, input_data=input_data, timeout=timeout)

    def run(self, argv: list[str], *, input_data: bytes | None = None, timeout: float | None = None, cwd: Path | None = None) -> CommandOutput:
        started = time.monotonic()
        try:
            proc = subprocess.run(
                argv,
                cwd=cwd or self.workdir,
                env=self.env,
                input=input_data,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                timeout=timeout or self.timeout,
            )
            return CommandOutput(argv, proc.returncode, proc.stdout, proc.stderr, time.monotonic() - started)
        except subprocess.TimeoutExpired as exc:
            return CommandOutput(argv, 124, exc.stdout or b"", exc.stderr or b"", time.monotonic() - started)

    def spawn_pty(self, action: str, *, rows: int = 24, cols: int = 80, extra: list[str] | None = None, **values: Any) -> PTYProcess:
        argv = self.command(action, **values)
        if extra:
            argv.extend(extra)
        return PTYProcess(argv, self.workdir, self.env, rows=rows, cols=cols)

    def new_session(self, name: str, command: list[str], timeout: float | None = None) -> CommandOutput:
        self.sessions.add(name)
        return self.run_action("new", name=name, extra=command, timeout=timeout)

    def result(self, name: str, passed: bool, detail: str, started: float, stdout: bytes = b"", stderr: bytes = b"") -> CheckResult:
        return CheckResult(
            name=name,
            passed=passed,
            detail=detail,
            duration_seconds=time.monotonic() - started,
            stdout=stdout.decode("utf-8", "replace"),
            stderr=stderr.decode("utf-8", "replace"),
        )

    def require_tools(self, *names: str) -> tuple[bool, str]:
        missing = [name for name in names if shutil.which(name) is None]
        return (not missing, "missing tools: " + ", ".join(missing) if missing else "")
