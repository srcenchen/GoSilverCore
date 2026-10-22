package webui

import (
	"os/exec"
	"strconv"
)

func configureCommand(c *exec.Cmd) {
	c.Cancel = func() error {
		if c.Process == nil {
			return nil
		}
		return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(c.Process.Pid)).Run()
	}
}
