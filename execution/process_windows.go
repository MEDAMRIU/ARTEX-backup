//go:build windows

package execution

import "os/exec"

func configureSandboxProcess(cmd *exec.Cmd) {}
func killSandbox(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
