package util

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Sha256File hashes a file in 1 MiB chunks.
func Sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.CopyBuffer(h, f, make([]byte, 1<<20)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// GitCommit returns HEAD in dir or "".
func GitCommit(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(out.String())
}

// GetAny returns the first alias present in obj.
func GetAny(obj map[string]any, aliases []string) any {
	for _, k := range aliases {
		if v, ok := obj[k]; ok {
			return v
		}
	}
	return nil
}

// StringAliases extracts []string from config json_fields entries.
func StringAliases(cfg map[string]any, path ...string) []string {
	cur := any(cfg)
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	arr, ok := cur.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// ParseJSONOutput parses stripped stdout as JSON.
func ParseJSONOutput(text string) (any, error) {
	var v any
	dec := json.NewDecoder(strings.NewReader(strings.TrimSpace(text)))
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// ProcessAlive reports liveness. On Linux a zombie (state Z) counts as dead.
// Portable: falls back to kill(pid, 0) on darwin.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if stat, err := os.ReadFile(filepath.Join("/proc", itoa(pid), "stat")); err == nil {
		// Format: pid (comm) state ...
		s := string(stat)
		if idx := strings.LastIndex(s, ")"); idx >= 0 && idx+2 < len(s) {
			rest := strings.TrimSpace(s[idx+1:])
			if strings.HasPrefix(rest, "Z") {
				return false
			}
		}
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// WaitUntil polls predicate until timeout.
func WaitUntil(pred func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return pred()
}

// CleanupRuntimeProcesses SIGTERMs then SIGKILLs processes whose environ
// carries the test runtime. Linux (/proc) only; no-op elsewhere.
func CleanupRuntimeProcesses(runtime string) {
	if _, err := os.Stat("/proc"); err != nil {
		return
	}
	needles := [][]byte{
		[]byte("XDG_RUNTIME_DIR=" + runtime),
		[]byte("DMUX_RUNTIME_DIR=" + filepath.Join(runtime, "dmux")),
	}
	self := os.Getpid()
	var victims []int
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	for _, e := range entries {
		if !isDigits(e.Name()) {
			continue
		}
		pid := atoi(e.Name())
		if pid == self || pid <= 0 {
			continue
		}
		env, err := os.ReadFile(filepath.Join("/proc", e.Name(), "environ"))
		if err != nil {
			continue
		}
		parts := bytes.Split(env, []byte{0})
		hit := false
		for _, p := range parts {
			for _, n := range needles {
				if bytes.Equal(p, n) {
					hit = true
					break
				}
			}
			if hit {
				break
			}
		}
		if hit {
			victims = append(victims, pid)
		}
	}
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		for _, pid := range victims {
			_ = syscall.Kill(pid, sig)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func atoi(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		n = n*10 + int(s[i]-'0')
	}
	return n
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
