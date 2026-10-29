package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	yaml "gopkg.in/yaml.v3"
)

// Three surfaces write this file — the web UI, the CLI and the operator's own
// editor — and each writes it WHOLE. Without a lock and a check, two of them
// editing different instances silently discard each other's work: last writer
// wins, and nothing says so.
//
// So a write takes an exclusive lock on a sidecar (the config itself is replaced
// by rename, so a lock on its inode would not survive) and refuses when the file
// has changed since the caller read it.

// ErrChanged means the file moved under the caller. Re-read and re-apply; do not
// overwrite, because the other change was somebody's deliberate edit too.
var ErrChanged = errors.New("конфиг на диске изменился после чтения")

// Fingerprint identifies the exact bytes on disk. A missing file fingerprints as
// the empty string, which is also what a caller passes when it expects to be
// creating the file.
func Fingerprint(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// SaveIf writes the config only if the file still looks the way the caller last
// saw it.
func SaveIf(path string, c *Config, expect string) error {
	unlock, err := lockConfig(path)
	if err != nil {
		return err
	}
	defer unlock()

	current, err := Fingerprint(path)
	if err != nil {
		return fmt.Errorf("чтение текущего конфига: %w", err)
	}
	if current != expect {
		return fmt.Errorf("%w — перечитайте и примените изменение заново", ErrChanged)
	}
	return replaceConfigFile(path, c)
}

// Save writes unconditionally. It is the right call only when the caller owns
// the file outright — a first-time write, or a CLI that just read it under the
// same lock.
func Save(path string, c *Config) error {
	unlock, err := lockConfig(path)
	if err != nil {
		return err
	}
	defer unlock()
	return replaceConfigFile(path, c)
}

// replaceConfigFile replaces the file atomically. A read-only mount fails here
// with a plain error rather than pretending the save worked.
//
// It refuses first: a config the agent cannot LOAD must not reach the disk. A
// running connector reloads from this file, and a file it refuses leaves it on
// the previous config — so the write appears to succeed, the page shows the
// change, and the connector quietly keeps doing the old thing. Every writer
// goes through here, so this is the one place that has to say no.
func replaceConfigFile(path string, c *Config) error {
	if err := validForWrite(c); err != nil {
		return fmt.Errorf("не записываю конфиг, который коннектор не загрузит: %w", err)
	}
	b, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("сборка конфига: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("запись конфига (каталог только для чтения?): %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("замена конфига: %w", err)
	}
	return nil
}

func lockPath(path string) string {
	return filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".lock")
}

// validForWrite asks the question the write is really asking: would Load accept
// this? Load fills the baked-in relay before it validates, so a config that
// legitimately omits middleware_url must not be refused here — the check has to
// mirror the load path, not merely resemble it.
func validForWrite(c *Config) error {
	check := *c
	if check.MiddlewareURL == "" {
		check.MiddlewareURL = DefaultRelay
	}
	return check.Validate()
}

// CheckWritable is validForWrite for callers that want the verdict WITHOUT the
// write — a dry run that says "fine" and then a real run that refuses is a dry
// run that lied.
func (c *Config) CheckWritable() error { return validForWrite(c) }
