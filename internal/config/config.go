package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// DefaultJSON mirrors dmux_verify/config.py DEFAULT_CONFIG.
const DefaultJSON = `{
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
}`

// Default returns a deep copy of the default config.
func Default() map[string]any {
	var v map[string]any
	if err := json.Unmarshal([]byte(DefaultJSON), &v); err != nil {
		panic(err)
	}
	return v
}

// WriteDefault writes the default config, overriding binary when non-empty.
func WriteDefault(path string, binary string) error {
	cfg := Default()
	if binary != "" {
		cfg["binary"] = binary
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// Load reads user config and deep-merges it over defaults.
func Load(path string) (map[string]any, error) {
	base := Default()
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var user map[string]any
	if err := json.Unmarshal(raw, &user); err != nil {
		return nil, err
	}
	merge(base, user)
	return base, nil
}

func merge(base, override map[string]any) {
	for k, v := range override {
		ov, ok := v.(map[string]any)
		bv, bok := base[k].(map[string]any)
		if ok && bok {
			merge(bv, ov)
		} else {
			base[k] = v
		}
	}
}
