//go:build !windows

package config

import (
	"fmt"
	"os"
	"syscall"
)

// lockConfig takes an exclusive advisory lock shared by every writer of this
// config — the web UI, the CLI, and anything else we add later.
func lockConfig(path string) (func(), error) {
	f, err := os.OpenFile(lockPath(path), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open config lock (is the config directory read-only?): %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock config: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
