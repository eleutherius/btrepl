package systemd

import (
	"fmt"
	"os/exec"
	"strings"
)

func ctl(args ...string) error {
	cmd := exec.Command("systemctl", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return nil
}

func Enable(unit string) error  { return ctl("enable", unit) }
func Disable(unit string) error { return ctl("disable", unit) }
func Start(unit string) error   { return ctl("start", unit) }
func Stop(unit string) error    { return ctl("stop", unit) }
func Restart(unit string) error { return ctl("restart", unit) }

// IsActive returns true when the unit is currently active.
func IsActive(unit string) (bool, error) {
	cmd := exec.Command("systemctl", "is-active", "--quiet", unit)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if _, ok := err.(*exec.ExitError); ok {
		return false, nil
	}
	return false, err
}
