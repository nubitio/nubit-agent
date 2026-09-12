//go:build !darwin && !linux

package durable

import "errors"

// ErrWriterLockUnsupported is returned on targets without flock.
var ErrWriterLockUnsupported = errors.New("writer lock is unsupported on this platform")
var ErrWriterBusy = errors.New("another nubit-agent writer is active")

type WriterLock struct{}

func Acquire(string) (*WriterLock, error) { return nil, ErrWriterLockUnsupported }
func (*WriterLock) Close() error          { return nil }
