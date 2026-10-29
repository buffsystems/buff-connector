package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	yaml "gopkg.in/yaml.v3"
)

// DefaultStateDir is the documented home of the connector's state: the terminal
// wizard offers it and the image mounts its volume there.
const DefaultStateDir = "/var/lib/connector"

// fallbackStateDir is the state directory's name next to the config when the
// documented one is out of reach.
const fallbackStateDir = "connector-state"

// CreateIfMissing writes a minimal config at path when there is none, and
// returns it; an existing file is left exactly as it is and nil is returned.
//
// `pair` is the first command a new operator runs (autonomous-dev/cloud-front#628):
// the relay is baked into the build, so the only setting a connector cannot do
// without is state_dir. Asking the operator to write that one line by hand made
// the very first command fail on a clean machine.
//
// The state directory is DefaultStateDir when this user can write there — a
// root install gets the documented layout. Otherwise (an ordinary user, the
// cabinet's `connector pair --config connector.yaml`) it is connector-state
// beside the config, as an absolute path so `run` from any directory finds it.
func CreateIfMissing(path string) (*Config, error) {
	return createIfMissing(path, usableDir)
}

func createIfMissing(path string, usable func(string) bool) (*Config, error) {
	if _, err := os.Stat(path); err == nil || !errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	stateDir := DefaultStateDir
	if !usable(stateDir) {
		dir, err := filepath.Abs(filepath.Dir(path))
		if err != nil {
			return nil, fmt.Errorf("каталог конфига: %w", err)
		}
		stateDir = filepath.Join(dir, fallbackStateDir)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("создание каталога конфига: %w", err)
	}
	b, err := yaml.Marshal(struct {
		StateDir string `yaml:"state_dir"`
	}{stateDir})
	if err != nil {
		return nil, fmt.Errorf("сборка конфига: %w", err)
	}
	// O_EXCL: a file that appeared since the check above is somebody's config,
	// never overwritten.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("запись конфига (каталог только для чтения?): %w", err)
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("запись конфига (каталог только для чтения?): %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("запись конфига (каталог только для чтения?): %w", err)
	}
	return &Config{StateDir: stateDir}, nil
}

// usableDir reports whether this process can keep its state in dir, creating
// it if it does not exist yet.
func usableDir(dir string) bool {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false
	}
	probe, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		return false
	}
	_ = probe.Close()
	_ = os.Remove(probe.Name())
	return true
}
