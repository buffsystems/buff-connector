//go:build windows

package plugin

import (
	"github.com/buffsystems/buff-connector/internal/problem"
	"os/exec"
)

// Windows has no equivalent we are willing to ship yet: running a child under
// another account needs LogonUser/CreateProcessAsUser, i.e. a local service
// account the operator creates AND its password handed to the connector. Until
// that is a deliberate installation step, plugins do not run on Windows — the
// connector says so rather than running them unisolated.
func CheckPrivilege() Privilege {
	return Privilege{
		Reason: problem.New("no_isolation_windows"),
	}
}

func applyProcAttr(*exec.Cmd, uint32, uint32, bool) {}

func terminate(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

func chownDir(string, uint32, uint32) error { return nil }
