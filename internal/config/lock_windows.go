//go:build windows

package config

// Windows has no flock. The connector does not run plugins there yet, so the
// concurrent-writer problem this guards against does not arise; when Windows
// plugin support lands this needs LockFileEx.
func lockConfig(string) (func(), error) { return func() {}, nil }
