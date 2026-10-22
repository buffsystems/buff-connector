package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// ErrNoPrivilege means the connector cannot put a plugin under its own OS user.
// We refuse to run plugins at all in that case: running them under the
// connector's own uid would give them the identity seed and every provider token,
// which is exactly the isolation we promise.
var ErrNoPrivilege = errors.New("cannot run plugins isolated")

// Privilege is what this process is actually able to do, decided once at start
// so the operator learns about it immediately instead of at the first install.
type Privilege struct {
	CanSwitchUID bool
	// Reason explains a refusal in the operator's terms, e.g. "connector runs as
	// uid 1000; plugin isolation needs root or CAP_SETUID".
	Reason string
}

// Spec describes one plugin process.
type Spec struct {
	Path string
	Args []string
	Dir  string
	Env  []string
	UID  uint32
	GID  uint32
}

// Starter launches plugin processes. Privilege is injected rather than read from
// the OS on every call, so the refusal path is testable and the decision is made
// in exactly one place.
type Starter struct {
	Priv Privilege
	// GraceDuration is how long a plugin gets to exit after the polite signal
	// before it is killed. Zero means a sensible default.
	GraceDuration time.Duration
}

// Process is a running plugin. Stdout/Stdin carry the JSON-RPC channel; stderr is
// left to the caller to drain into the instance log.
type Process struct {
	cmd    *exec.Cmd
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
	Stderr io.ReadCloser
	grace  time.Duration
	done   chan struct{}
	once   sync.Once
	err    error
}

// Start runs the plugin. When a uid is requested but cannot be applied, nothing
// is executed: an unisolated plugin is not a degraded plugin, it is a different
// security promise.
func (s *Starter) Start(ctx context.Context, spec Spec) (*Process, error) {
	if spec.UID != 0 && !s.Priv.CanSwitchUID {
		reason := s.Priv.Reason
		if reason == "" {
			reason = "this process cannot change a child's user"
		}
		return nil, fmt.Errorf("%w: %s", ErrNoPrivilege, reason)
	}

	cmd := exec.CommandContext(ctx, spec.Path, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	applyProcAttr(cmd, spec.UID, spec.GID, spec.UID != 0)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", spec.Path, err)
	}

	grace := s.GraceDuration
	if grace == 0 {
		grace = 5 * time.Second
	}
	// Deliberately NOT reaping here: exec closes the pipes in Wait, and calling it
	// while someone is still reading stdout loses whatever the plugin had already
	// written. The owner reaps once it has drained the channel.
	return &Process{cmd: cmd, Stdin: stdin, Stdout: stdout, Stderr: stderr,
		grace: grace, done: make(chan struct{})}, nil
}

// reap collects the exit status exactly once. Callers reach it through Wait or
// Stop; both are safe to call from several goroutines.
func (p *Process) reap() {
	p.once.Do(func() {
		p.err = p.cmd.Wait()
		close(p.done)
	})
}

// Wait blocks until the process exits. Call it only after the caller has
// finished reading Stdout/Stderr — Wait closes those pipes.
func (p *Process) Wait() error {
	p.reap()
	return p.err
}

// Exited reports whether the process has already gone.
func (p *Process) Exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// Stop asks the plugin to exit, then insists. A plugin that ignores the polite
// signal must not survive: it holds an open conversation with a customer system.
func (p *Process) Stop() {
	if p.cmd.Process == nil {
		return
	}
	_ = p.Stdin.Close()
	_ = terminate(p.cmd)
	go p.reap()
	select {
	case <-p.done:
		return
	case <-time.After(p.grace):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

// PrepareStateDir lays out the connector's state directory so a plugin running
// as another user can reach its own code and state and nothing else.
//
// The modes are the point, and they were verified against a real child process:
//
//	<root>            0755  traversable, so a plugin can descend
//	<root>/identity   0700  core only — holds the signing seed
//	<root>/secrets    0700  core only — holds provider and integration tokens
//	<root>/plugins    0755  readable — plugin code and the interpreter
//	<root>/instances  0711  traversable but NOT listable: an instance cannot even
//	                        enumerate its neighbours
func PrepareStateDir(root string) error {
	dirs := []struct {
		name string
		mode os.FileMode
	}{
		{"", 0o755},
		{"identity", 0o700},
		{"secrets", 0o700},
		{"plugins", 0o755},
		{"runtime", 0o755},
		{"instances", 0o711},
	}
	for _, d := range dirs {
		path := filepath.Join(root, d.name)
		if err := os.MkdirAll(path, d.mode); err != nil {
			return fmt.Errorf("create %s: %w", path, err)
		}
		// MkdirAll honours umask, so set the mode explicitly.
		if err := os.Chmod(path, d.mode); err != nil {
			return fmt.Errorf("chmod %s: %w", path, err)
		}
	}
	return nil
}

// PrepareCodeDir creates the directory a custom server's code is unpacked into
// (#351), owned by the connection's user: installing the server's dependencies
// runs as that user, in that directory, and must be able to write there. The
// parent is traversable but not listable, like the instances' — a server
// cannot enumerate its neighbours' code either.
func PrepareCodeDir(root, guid string, uid, gid uint32) (string, error) {
	parent := filepath.Join(root, "mcp")
	if err := os.MkdirAll(parent, 0o711); err != nil {
		return "", fmt.Errorf("create %s: %w", parent, err)
	}
	if err := os.Chmod(parent, 0o711); err != nil {
		return "", fmt.Errorf("chmod %s: %w", parent, err)
	}
	dir := filepath.Join(parent, guid)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("chmod %s: %w", dir, err)
	}
	if err := chownDir(dir, uid, gid); err != nil {
		return "", err
	}
	return dir, nil
}

// ChownTree hands a whole tree to a user — what an unpacked archive needs
// before that user installs into it.
func ChownTree(dir string, uid, gid uint32) error {
	return filepath.WalkDir(dir, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return chownDir(path, uid, gid)
	})
}

// PrepareInstanceDir creates one instance's private state directory, owned by
// that instance's user. Ownership is per instance, not per plugin: two instances
// of the same plugin hold different credentials and must not reach each other.
func PrepareInstanceDir(root, guid string, uid, gid uint32) (string, error) {
	dir := filepath.Join(root, "instances", guid)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("chmod %s: %w", dir, err)
	}
	if err := chownDir(dir, uid, gid); err != nil {
		return "", err
	}
	return dir, nil
}
