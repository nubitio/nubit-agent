// Package durable contains the filesystem guarantees shared by the agent's
// local stores. It intentionally has no knowledge of a particular store
// format.
package durable

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// CommitError reports an error after the replacement was renamed into place.
// Callers must not roll back their in-memory value for this error.
type CommitError struct{ Err error }

func (err *CommitError) Error() string { return err.Err.Error() }
func (err *CommitError) Unwrap() error { return err.Err }

func IsCommitted(err error) bool {
	var commitErr *CommitError
	return errors.As(err, &commitErr)
}

var syncDirectory = func(dir *os.File) error { return dir.Sync() }

// AtomicWrite replaces path only after the complete new value is written and
// synced. Syncing the parent directory makes the rename durable on filesystems
// that support directory fsync (the error is returned rather than hidden).
func AtomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(filepath.Dir(path), ".nubit-tmp-"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		// A fixed temporary name is not safe when a stale file is left after a
		// kill. Use CreateTemp after the fast path fails.
		f, err = os.CreateTemp(filepath.Dir(path), ".nubit-tmp-")
		if err != nil {
			return err
		}
		if chmodErr := f.Chmod(mode); chmodErr != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
			return chmodErr
		}
	}
	temporary := f.Name()
	cleanup := func() { _ = f.Close(); _ = os.Remove(temporary) }
	if _, err := f.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	err = syncDirectory(dir)
	closeErr := dir.Close()
	if err != nil {
		return &CommitError{Err: err}
	}
	if closeErr != nil {
		return &CommitError{Err: closeErr}
	}
	return nil
}

// CopyBounded copies at most limit bytes and reports whether more data exists.
func CopyBounded(dst io.Writer, src io.Reader, limit int64) (bool, error) {
	n, err := io.CopyN(dst, src, limit+1)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return false, err
	}
	return n > limit, nil
}

func Invalid(path string, err error) error {
	return fmt.Errorf("durable store %s is invalid: %w", path, err)
}
