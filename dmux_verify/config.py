from __future__ import annotations

import json
from pathlib import Path
from typing import Any

DEFAULT_CONFIG: dict[str, Any] = {
    "binary": "./dmux",
    "working_directory": ".",
    "timeout_seconds": 8,
    "detach_sequence": "\u0001d",
    "runtime_environment": {
        "XDG_RUNTIME_DIR": "{temp_runtime}",
        "DMUX_RUNTIME_DIR": "{temp_runtime}/dmux"
    },
    "commands": {
        "version": ["version"],
        "help": ["help"],
        "run": ["run", "--"],
        "new": ["new", "{name}", "--"],
        "attach": ["attach", "{name}"],
        "list": ["list", "--json"],
        "inspect": ["inspect", "{name}", "--json"],
        "logs": ["logs", "{name}", "--tail", "{tail}"],
        "kill": ["kill", "{name}"],
        "daemon_start": ["daemon", "start"],
        "daemon_stop": ["daemon", "stop"]
    },
    "json_fields": {
        "sessions_container": ["sessions", "items", "data"],
        "id": ["id", "session_id", "sessionId"],
        "name": ["name", "session_name", "sessionName"],
        "command": ["command", "cmd", "argv"],
        "created_at": ["created_at", "createdAt", "created"],
        "attached_clients": ["attached_clients", "attachedClients", "clients"],
        "state": ["state", "status"],
        "exit_code": ["exit_code", "exitCode", "code"],
        "signal": ["signal", "signal_name", "signalName"]
    },
    "custom_checks": {}
}


def _deep_copy(value: Any) -> Any:
    return json.loads(json.dumps(value))


def write_default_config(path: Path, binary: str | None = None) -> None:
    config = _deep_copy(DEFAULT_CONFIG)
    if binary:
        config["binary"] = binary
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(config, indent=2) + "\n", encoding="utf-8")


def load_config(path: Path) -> dict[str, Any]:
    if not path.exists():
        raise FileNotFoundError(f"Verifier is not initialised: {path} does not exist")
    user = json.loads(path.read_text(encoding="utf-8"))
    config = _deep_copy(DEFAULT_CONFIG)
    _merge(config, user)
    return config


def _merge(base: dict[str, Any], override: dict[str, Any]) -> None:
    for key, value in override.items():
        if isinstance(value, dict) and isinstance(base.get(key), dict):
            _merge(base[key], value)
        else:
            base[key] = value
