package enrollment

import (
	"os"
	"path/filepath"
)

// writeAtomic writes contents to path via a temporary file in the same
// directory and a rename. Rename is atomic on POSIX filesystems, so a
// partially-written or stale certificate can never be observed by a reader
// that opens the final name. The temporary file is removed on any error
// path so the directory does not accumulate debris.
func writeAtomic(path string, contents []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".nubit-enrollment-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
