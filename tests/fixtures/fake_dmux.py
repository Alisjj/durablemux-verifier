#!/usr/bin/env python3
import os
import sys


def main():
    args = sys.argv[1:]
    if args == ["version"]:
        print("fake-dmux 0.1.0")
        return 0
    if args == ["help"]:
        print("commands: version help run")
        return 0
    if args and args[0] == "run":
        command = args[1:]
        if command and command[0] == "--":
            command = command[1:]
        if not command:
            print("missing command", file=sys.stderr)
            return 2
        try:
            os.execvp(command[0], command)
        except FileNotFoundError:
            print(f"executable not found: {command[0]}", file=sys.stderr)
            return 127
    print("unknown command", file=sys.stderr)
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
