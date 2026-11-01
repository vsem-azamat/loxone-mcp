package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockStateDir makes sure only one server uses the state directory: two would share one token
// and one client identity on the Miniserver and send every write twice.
func lockStateDir(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("state directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // a directory needs x; 0700 is owner only
		return nil, fmt.Errorf("state directory: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "serve.lock"), os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // the state directory is configuration
	if err != nil {
		return nil, fmt.Errorf("state directory lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("another loxone-mcp serve uses %s", dir)
		}
		return nil, fmt.Errorf("state directory lock: %w", err)
	}
	return func() { f.Close() }, nil
}
