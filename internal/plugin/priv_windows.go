//go:build windows

package plugin

import "os/exec"

// Windows has no equivalent we are willing to ship yet: running a child under
// another account needs LogonUser/CreateProcessAsUser, i.e. a local service
// account the operator creates AND its password handed to the connector. Until
// that is a deliberate installation step, plugins do not run on Windows — the
// connector says so rather than running them unisolated.
func CheckPrivilege() Privilege {
	return Privilege{
		Reason: "plugins are not supported on Windows yet: isolating them needs a " +
			"dedicated local service account, which the installer does not create",
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
