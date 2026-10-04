package checks

import (
	"os"
	"path/filepath"
)

func runtimeSockets(root string) (int, error) {
	count := 0
	err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSocket != 0 {
			count++
		}
		return nil
	})
	return count, err
}
