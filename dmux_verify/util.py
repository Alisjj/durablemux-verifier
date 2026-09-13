from __future__ import annotations

import hashlib
import json
import os
import signal
import subprocess
import time
from pathlib import Path
from typing import Any


def sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def git_commit(cwd: Path) -> str | None:
    try:
        proc = subprocess.run(
            ["git", "rev-parse", "HEAD"], cwd=cwd, text=True,
            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=2
        )
    except (OSError, subprocess.TimeoutExpired):
        return None
    return proc.stdout.strip() if proc.returncode == 0 else None


def get_any(obj: dict[str, Any], aliases: list[str]) -> Any:
    for key in aliases:
        if key in obj:
            return obj[key]
    return None


def parse_json_output(text: str) -> Any:
    return json.loads(text.strip())


def process_alive(pid: int) -> bool:
    try:
        stat = Path(f"/proc/{pid}/stat").read_text(encoding="utf-8")
        right = stat.rsplit(")", 1)[1].strip().split()
        if right and right[0] == "Z":
            return False
    except (FileNotFoundError, PermissionError, IndexError):
        pass
    try:
        os.kill(pid, 0)
        return True
    except ProcessLookupError:
        return False
    except PermissionError:
        return True


def wait_until(predicate, timeout: float, interval: float = 0.05) -> bool:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if predicate():
            return True
        time.sleep(interval)
    return bool(predicate())


def cleanup_runtime_processes(runtime: Path) -> None:
    if not Path("/proc").exists():
        return
    needles = {
        f"XDG_RUNTIME_DIR={runtime}".encode(),
        f"DMUX_RUNTIME_DIR={runtime / 'dmux'}".encode(),
    }
    victims: list[int] = []
    for entry in Path("/proc").iterdir():
        if not entry.name.isdigit():
            continue
        pid = int(entry.name)
        if pid == os.getpid():
            continue
        try:
            env = (entry / "environ").read_bytes().split(b"\0")
        except (FileNotFoundError, PermissionError, ProcessLookupError):
            continue
        if any(n in env for n in needles):
            victims.append(pid)
    for sig in (signal.SIGTERM, signal.SIGKILL):
        for pid in victims:
            try:
                os.kill(pid, sig)
            except (ProcessLookupError, PermissionError):
                pass
        time.sleep(0.15)
