package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Alisjj/durablemux-verifier/internal/proctree"
	"github.com/Alisjj/durablemux-verifier/internal/runner"
	"github.com/Alisjj/durablemux-verifier/internal/store"
	"github.com/Alisjj/durablemux-verifier/internal/util"
)

func (ui *reviewUI) captureProcessTree(ctx *runner.Context, s *store.Store, root, meta string) (string, error) {
	fmt.Fprintln(ui.output, "Starting the foreground process-tree experiment and capturing it while sleep 30 is running...")
	data, err := proctree.Capture(ctx)
	if err != nil {
		return "", err
	}
	if err := proctree.ValidateText(data); err != nil {
		return "", err
	}
	fmt.Fprintf(ui.output, "%s\n", data)
	dir := filepath.Join(meta, "evidence", "stage-04")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	file := filepath.Join(dir, time.Now().UTC().Format("20060102T150405.000000000Z")+"-process-tree.txt")
	if err := os.WriteFile(file, data, 0o644); err != nil {
		return "", err
	}
	sha, err := util.Sha256File(file)
	if err != nil {
		return "", err
	}
	path, err := filepath.Rel(root, file)
	if err != nil {
		return "", err
	}
	if err := s.AddEvidence(4, map[string]any{"at": store.UTCNow(), "type": "process_tree", "note": "Shell and foreground command captured while sleep 30 was active", "path": path, "sha256": sha, "exit_code": 0}); err != nil {
		return "", err
	}
	return path, nil
}
