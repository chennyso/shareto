package service

import (
	"errors"
	"os/exec"
)

func Active() (bool, error) {
	err := runCommand("systemctl", "--user", "is-active", "--quiet", "shareto.service")
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && (exit.ExitCode() == 3 || exit.ExitCode() == 4) {
		return false, nil
	}
	return false, err
}

func Start() error {
	return runCommand("systemctl", "--user", "start", "shareto.service")
}

func Stop() error {
	return runCommand("systemctl", "--user", "stop", "shareto.service")
}

func Restart() error {
	return runCommand("systemctl", "--user", "restart", "shareto.service")
}

func Install() error {
	return runCommand("systemctl", "--user", "enable", "--now", "shareto.service")
}

func Uninstall() error {
	return runCommand("systemctl", "--user", "disable", "--now", "shareto.service")
}

func Instructions() string {
	return "Run 'shareto service install' to enable the systemd user service, or 'shareto daemon' to run in the foreground."
}
