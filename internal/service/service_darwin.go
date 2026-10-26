package service

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"howett.net/plist"
)

const agentLabel = "io.github.huadaox.shareto"

type agentConfig struct {
	label       string
	plistPath   string
	executable  string
	logDir      string
	workingDir  string
	environment map[string]string
}

func defaultAgent() (agentConfig, error) {
	if os.Geteuid() == 0 {
		return agentConfig{}, fmt.Errorf("install the launch agent as your logged-in user without sudo")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return agentConfig{}, err
	}
	executable, err := os.Executable()
	if err != nil {
		return agentConfig{}, err
	}
	environment := make(map[string]string)
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR"} {
		if value := os.Getenv(key); value != "" {
			if !filepath.IsAbs(value) {
				return agentConfig{}, fmt.Errorf("%s must be an absolute directory path", key)
			}
			environment[key] = value
		}
	}
	return agentConfig{
		label:       agentLabel,
		plistPath:   filepath.Join(home, "Library", "LaunchAgents", agentLabel+".plist"),
		executable:  executable,
		logDir:      filepath.Join(home, "Library", "Logs", "shareto"),
		workingDir:  home,
		environment: environment,
	}, nil
}

func agentDomain() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

func agentActive(label string) (bool, error) {
	err := runCommand("/bin/launchctl", "print", agentDomain()+"/"+label)
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 113 {
		return false, nil
	}
	return false, err
}

func writeAgent(agent agentConfig) error {
	if !filepath.IsAbs(agent.executable) || !filepath.IsAbs(agent.plistPath) || !filepath.IsAbs(agent.logDir) || !filepath.IsAbs(agent.workingDir) || agent.label == "" {
		return fmt.Errorf("launch agent requires an identifier and absolute executable, plist and log paths")
	}
	if err := os.MkdirAll(filepath.Dir(agent.plistPath), 0700); err != nil {
		return err
	}
	if err := os.MkdirAll(agent.logDir, 0700); err != nil {
		return err
	}
	definition := map[string]interface{}{
		"Label":                agent.label,
		"ProgramArguments":     []string{agent.executable, "daemon"},
		"RunAtLoad":            true,
		"KeepAlive":            true,
		"ThrottleInterval":     10,
		"ProcessType":          "Background",
		"Umask":                0o077,
		"StandardOutPath":      filepath.Join(agent.logDir, "stdout.log"),
		"StandardErrorPath":    filepath.Join(agent.logDir, "stderr.log"),
		"EnvironmentVariables": agent.environment,
		"WorkingDirectory":     agent.workingDir,
	}
	file, err := os.CreateTemp(filepath.Dir(agent.plistPath), ".shareto-agent-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	encoder := plist.NewEncoderForFormat(file, plist.XMLFormat)
	if err := encoder.Encode(definition); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), agent.plistPath); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(agent.plistPath))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func installAgent(agent agentConfig) error {
	if err := writeAgent(agent); err != nil {
		return err
	}
	if err := stopAgent(agent.label); err != nil {
		return err
	}
	if err := runCommand("/bin/launchctl", "bootstrap", agentDomain(), agent.plistPath); err != nil {
		return err
	}
	return runCommand("/bin/launchctl", "kickstart", "-p", agentDomain()+"/"+agent.label)
}

func stopAgent(label string) error {
	active, err := agentActive(label)
	if err != nil || !active {
		return err
	}
	if err := runCommand("/bin/launchctl", "bootout", agentDomain()+"/"+label); err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		active, err := agentActive(label)
		if err != nil {
			return err
		}
		if !active {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("launch agent %s did not stop within ten seconds", label)
}

func Active() (bool, error) {
	return agentActive(agentLabel)
}

func Install() error {
	agent, err := defaultAgent()
	if err != nil {
		return err
	}
	return installAgent(agent)
}

func Start() error {
	active, err := Active()
	if err != nil {
		return err
	}
	if !active {
		return Install()
	}
	return runCommand("/bin/launchctl", "kickstart", "-p", agentDomain()+"/"+agentLabel)
}

func Stop() error {
	return stopAgent(agentLabel)
}

func Restart() error {
	return Install()
}

func Uninstall() error {
	if err := Stop(); err != nil {
		return err
	}
	agent, err := defaultAgent()
	if err != nil {
		return err
	}
	if err := os.Remove(agent.plistPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func Instructions() string {
	return "Run 'shareto service install' as your logged-in macOS user to enable the launchd agent, or 'shareto daemon' to run in the foreground."
}
