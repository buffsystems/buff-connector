//go:build !windows

package plugin

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/buffsystems/buff-connector/internal/problem"
)

// CheckPrivilege decides once whether this process can put a child under another
// user. On Unix that needs root (or CAP_SETUID, which root already implies for
// our purposes); a bare binary under an operator account and `docker run --user`
// both land here with CanSwitchUID false.
func CheckPrivilege() Privilege {
	if os.Geteuid() == 0 {
		return Privilege{CanSwitchUID: true}
	}
	return Privilege{
		Reason: problem.New("no_isolation_uid", os.Geteuid()),
	}
}

// applyProcAttr always puts the plugin in its OWN process group, whether or not
// it also changes user. Without that the child shares our group, and signalling
// "the plugin" would signal the connector itself — a mistake that is silent
// until the day it kills the wrong process.
func applyProcAttr(cmd *exec.Cmd, uid, gid uint32, setUser bool) {
	attr := &syscall.SysProcAttr{Setpgid: true}
	if setUser {
		attr.Credential = &syscall.Credential{Uid: uid, Gid: gid}
	}
	cmd.SysProcAttr = attr
}

// terminate signals the plugin's process group, so a Python plugin that spawned
// a helper does not leave it behind. It refuses to signal our own group, which
// would take the connector down with the plugin.
func terminate(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil || pgid == syscall.Getpgrp() {
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	return syscall.Kill(-pgid, syscall.SIGTERM)
}

func chownDir(dir string, uid, gid uint32) error {
	if os.Geteuid() != 0 {
		return nil // nothing to hand over; isolation is refused elsewhere
	}
	if err := os.Chown(dir, int(uid), int(gid)); err != nil {
		return problem.Wrap("dir_chown", err, dir)
	}
	return nil
}
