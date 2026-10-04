#!/usr/bin/env python3
"""A minimal genuine nested-PTY runner for verifier regression tests."""
import errno
import fcntl
import os
import pty
import select
import signal
import sys
import termios
import tty


def main():
    args = sys.argv[1:]
    if not args or args[0] != "run":
        print("unknown command", file=sys.stderr)
        return 2
    command = args[1:]
    if command and command[0] == "--":
        command = command[1:]
    if not os.isatty(0):
        os.execvp(command[0], command)
    original = termios.tcgetattr(0)
    size = fcntl.ioctl(0, termios.TIOCGWINSZ, b"\0" * 8)
    pid, master = pty.fork()
    if pid == 0:
        fcntl.ioctl(0, termios.TIOCSWINSZ, size)
        try:
            os.execvp(command[0], command)
        except OSError as error:
            print(error, file=sys.stderr, flush=True)
            os._exit(127)

    def resize(*_):
        fcntl.ioctl(master, termios.TIOCSWINSZ,
                    fcntl.ioctl(0, termios.TIOCGWINSZ, b"\0" * 8))

    def terminate(*_):
        try:
            os.killpg(pid, signal.SIGTERM)
        except ProcessLookupError:
            pass

    signal.signal(signal.SIGWINCH, resize)
    signal.signal(signal.SIGTERM, terminate)
    try:
        tty.setraw(0)
        while True:
            ready, _, _ = select.select([0, master], [], [], 0.1)
            if master in ready:
                try:
                    data = os.read(master, 65536)
                except OSError as error:
                    if error.errno == errno.EIO:
                        break
                    raise
                if not data:
                    break
                os.write(1, data)
            if 0 in ready:
                data = os.read(0, 4096)
                if not data:
                    terminate()
                    break
                os.write(master, data)
    finally:
        termios.tcsetattr(0, termios.TCSANOW, original)
        os.close(master)
    _, status = os.waitpid(pid, 0)
    if os.WIFSIGNALED(status):
        return 128 + os.WTERMSIG(status)
    return os.WEXITSTATUS(status)


if __name__ == "__main__":
    raise SystemExit(main())
