package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"shareto/internal/config"
	"shareto/internal/ipc"

	"howett.net/plist"
)

func TestLaunchAgentDefinition(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	agent := agentConfig{
		label:       "io.github.huadaox.shareto.test",
		plistPath:   filepath.Join(dir, "LaunchAgents", "shareto.plist"),
		executable:  executable,
		logDir:      filepath.Join(dir, "Logs & output"),
		workingDir:  dir,
		environment: map[string]string{"XDG_STATE_HOME": filepath.Join(dir, "State & data")},
	}
	if err := writeAgent(agent); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("/usr/bin/plutil", "-lint", agent.plistPath).CombinedOutput(); err != nil {
		t.Fatalf("invalid launchd plist: %s: %v", output, err)
	}
	data, err := os.ReadFile(agent.plistPath)
	if err != nil {
		t.Fatal(err)
	}
	var definition struct {
		Label                string
		ProgramArguments     []string
		RunAtLoad            bool
		KeepAlive            bool
		Umask                int
		EnvironmentVariables map[string]string
	}
	if _, err := plist.Unmarshal(data, &definition); err != nil {
		t.Fatal(err)
	}
	if definition.Label != agent.label || !reflect.DeepEqual(definition.ProgramArguments, []string{executable, "daemon"}) || !definition.RunAtLoad || !definition.KeepAlive || definition.Umask != 0o077 || !reflect.DeepEqual(definition.EnvironmentVariables, agent.environment) {
		t.Fatalf("unexpected launch agent definition: %+v", definition)
	}
	info, err := os.Stat(agent.plistPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("launch agent permissions: %o", info.Mode().Perm())
	}
}

func TestLaunchAgentRunsDaemonAndRestarts(t *testing.T) {
	if err := runCommand("/bin/launchctl", "print", agentDomain()); err != nil {
		t.Skipf("launchd GUI session is unavailable: %v", err)
	}
	dir := t.TempDir()
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(dir, "shareto")
	build := exec.Command("go", "build", "-o", executable, "./cmd/shareto")
	build.Dir = filepath.Join(workingDir, "..", "..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build actual daemon: %s: %v", output, err)
	}
	configDir := filepath.Join(dir, "config")
	stateDir := filepath.Join(dir, "state")
	runtimeDir := filepath.Join(dir, "runtime")
	cfg := config.DefaultConfig()
	cfg.ListenAddr = "127.0.0.1:0"
	if err := config.SaveConfig(filepath.Join(configDir, "shareto", "config.json"), cfg); err != nil {
		t.Fatal(err)
	}
	agent := agentConfig{
		label:       fmt.Sprintf("io.github.huadaox.shareto.test.%d", time.Now().UnixNano()),
		plistPath:   filepath.Join(dir, "agent.plist"),
		executable:  executable,
		logDir:      filepath.Join(dir, "logs"),
		workingDir:  dir,
		environment: map[string]string{"XDG_CONFIG_HOME": configDir, "XDG_STATE_HOME": stateDir, "XDG_RUNTIME_DIR": runtimeDir},
	}
	if err := installAgent(agent); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		active, err := agentActive(agent.label)
		if err != nil {
			t.Error(err)
		}
		if active {
			if err := stopAgent(agent.label); err != nil {
				t.Error(err)
			}
		}
	})
	client := ipc.NewClient(filepath.Join(runtimeDir, "shareto", "ipc.sock"))
	waitForPID := func(previous int) int {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if client.Ping() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				status, err := client.Status(ctx)
				cancel()
				if err == nil && status.PID > 0 && status.PID != previous {
					return status.PID
				}
			}
			time.Sleep(25 * time.Millisecond)
		}
		stdout, stdoutErr := os.ReadFile(filepath.Join(agent.logDir, "stdout.log"))
		stderr, stderrErr := os.ReadFile(filepath.Join(agent.logDir, "stderr.log"))
		diagnostic, diagnosticErr := exec.Command("/bin/launchctl", "print", agentDomain()+"/"+agent.label).CombinedOutput()
		t.Fatalf("launchd daemon did not become ready after PID %d; stdout=%s (%v); stderr=%s (%v); launchd=%s (%v)", previous, stdout, stdoutErr, stderr, stderrErr, diagnostic, diagnosticErr)
		return 0
	}
	firstPID := waitForPID(0)
	if err := installAgent(agent); err != nil {
		t.Fatal(err)
	}
	secondPID := waitForPID(firstPID)
	if err := stopAgent(agent.label); err != nil {
		t.Fatal(err)
	}
	if active, err := agentActive(agent.label); err != nil || active {
		t.Fatalf("launch agent remained loaded: active=%t err=%v", active, err)
	}
	t.Logf("launchd started daemon PID %d and restarted it as PID %d", firstPID, secondPID)
}

func TestLaunchAgentRejectsRelativePaths(t *testing.T) {
	if err := writeAgent(agentConfig{label: "shareto", plistPath: "agent.plist", executable: "shareto", logDir: "logs"}); err == nil {
		t.Fatal("relative launch agent paths were accepted")
	}
}
