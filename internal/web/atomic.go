// Package web is the client of the quet-web server: git-like remotes with their credentials, the per-labels-file
// link sidecar, an HTTP client for the admin API, and the push / pull operations shared by the CLI and the TUI.
package web

import (
	"fmt"
	"os"
	"path/filepath"
)

// writeFileAtomic writes data to path through a temp file in the same directory, so a reader never sees a partial
// file, and leaves the file with mode perm (independent of the umask). The parent directory must exist.
func writeFileAtomic(path string, data []byte, perm os.FileMode) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if err = tmp.Chmod(perm); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err = os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
