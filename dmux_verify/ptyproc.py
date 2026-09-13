from __future__ import annotations

import errno
import fcntl
import os
import pty
import select
import signal
import struct
import termios
import time
from pathlib import Path


class PTYProcess:
    def __init__(self, argv: list[str], cwd: Path, env: dict[str, str], rows: int = 24, cols: int = 80):
        self.argv = argv
        self.cwd = cwd
        self.env = env
        self.buffer = bytearray()
        self.returncode: int | None = None
        pid, master_fd = pty.fork()
        if pid == 0:
            try:
                os.chdir(cwd)
                os.execvpe(argv[0], argv, env)
            except BaseException as exc:
                os.write(2, f"exec failed: {exc}\n".encode())
                os._exit(127)
        self.pid = pid
        self.master_fd = master_fd
        self.set_size(rows, cols)

    def set_size(self, rows: int, cols: int) -> None:
        fcntl.ioctl(self.master_fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))

    def attrs(self):
        return termios.tcgetattr(self.master_fd)

    def write(self, data: bytes) -> None:
        os.write(self.master_fd, data)

    def read_some(self, timeout: float = 0.1) -> bytes:
        ready, _, _ = select.select([self.master_fd], [], [], timeout)
        if not ready:
            return b""
        try:
            data = os.read(self.master_fd, 65536)
        except OSError as exc:
            if exc.errno == errno.EIO:
                return b""
            raise
        self.buffer.extend(data)
        return data

    def read_until(self, needle: bytes, timeout: float) -> bytes:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if needle in self.buffer:
                return bytes(self.buffer)
            if self.poll() is not None:
                self.read_some(0)
                break
            self.read_some(min(0.1, max(0.0, deadline - time.monotonic())))
        if needle not in self.buffer:
            raise TimeoutError(f"did not observe {needle!r}; output={bytes(self.buffer)[-2000:]!r}")
        return bytes(self.buffer)

    def poll(self) -> int | None:
        if self.returncode is not None:
            return self.returncode
        try:
            pid, status = os.waitpid(self.pid, os.WNOHANG)
        except ChildProcessError:
            self.returncode = 0
            return self.returncode
        if pid == 0:
            return None
        if os.WIFEXITED(status):
            self.returncode = os.WEXITSTATUS(status)
        elif os.WIFSIGNALED(status):
            self.returncode = 128 + os.WTERMSIG(status)
        else:
            self.returncode = status
        return self.returncode

    def wait(self, timeout: float) -> int:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            rc = self.poll()
            if rc is not None:
                return rc
            self.read_some(0.05)
        raise TimeoutError(f"process did not exit: {self.argv}")

    def terminate(self) -> None:
        if self.poll() is not None:
            self.close()
            return
        for sig in (signal.SIGTERM, signal.SIGKILL):
            try:
                os.kill(self.pid, sig)
            except ProcessLookupError:
                break
            try:
                self.wait(0.5)
                break
            except TimeoutError:
                continue
        self.close()

    def close(self) -> None:
        try:
            os.close(self.master_fd)
        except OSError:
            pass
