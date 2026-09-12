//go:build darwin || linux

package durable

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

var ErrWriterBusy = errors.New("another nubit-agent writer is active")

type WriterLock struct{ file *os.File }

// Acquire prevents a second daemon or a node-local mutating maintenance
// command from touching the durable state at the same time.
func Acquire(path string) (*WriterLock, error) {
	if err := os.MkdirAll(filepathDir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open writer lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrWriterBusy
		}
		return nil, fmt.Errorf("lock writer state: %w", err)
	}
	return &WriterLock{file: f}, nil
}

func (lock *WriterLock) Close() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	err := syscall.Flock(int(lock.file.Fd()), syscall.LOCK_UN)
	closeErr := lock.file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func filepathDir(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			if i == 0 {
				return "/"
			}
			return path[:i]
		}
	}
	return "."
}
