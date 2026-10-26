package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"shareto/internal/config"
	"shareto/internal/ipc"
)

func TestCLIShareDownloadAndRevoke(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "shareto")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %s: %v", output, err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	cfg := config.DefaultConfig()
	cfg.ListenAddr = address
	cfg.PublicBaseURL = "http://" + address
	configDir := filepath.Join(dir, "config")
	runtimeDir := filepath.Join(dir, "run")
	if err := config.SaveConfig(filepath.Join(configDir, "shareto", "config.json"), cfg); err != nil {
		t.Fatal(err)
	}
	environment := append(os.Environ(),
		"XDG_CONFIG_HOME="+configDir,
		"XDG_STATE_HOME="+filepath.Join(dir, "state"),
		"XDG_RUNTIME_DIR="+runtimeDir,
	)
	log, err := os.Create(filepath.Join(dir, "daemon.log"))
	if err != nil {
		t.Fatal(err)
	}
	daemon := exec.Command(binary, "daemon")
	daemon.Env = environment
	daemon.Dir = dir
	daemon.Stdout = log
	daemon.Stderr = log
	if err := daemon.Start(); err != nil {
		log.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		daemon.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- daemon.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("daemon did not stop after SIGTERM")
			daemon.Process.Kill()
			<-done
		}
		log.Close()
	})
	client := ipc.NewClient(filepath.Join(runtimeDir, "shareto", "ipc.sock"))
	deadline := time.Now().Add(10 * time.Second)
	for !client.Ping() {
		if time.Now().After(deadline) {
			t.Fatal("daemon did not start")
		}
		time.Sleep(25 * time.Millisecond)
	}
	command := func(arguments ...string) string {
		cmd := exec.Command(binary, arguments...)
		cmd.Env = environment
		cmd.Dir = dir
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("CLI %v: %s: %v", arguments, output, err)
		}
		return string(output)
	}
	content := []byte("shareto real CLI integration content")
	file := filepath.Join(dir, "download.bin")
	if err := os.WriteFile(file, content, 0600); err != nil {
		t.Fatal(err)
	}
	command("-m", "1", file)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	shares, err := client.List(ctx)
	if err != nil || len(shares) != 1 {
		t.Fatalf("created shares: %v: %v", shares, err)
	}
	url := cfg.PublicBaseURL + "/s/" + shares[0].Token + "?raw=1"
	httpClient := &http.Client{Timeout: 5 * time.Second}
	defer httpClient.CloseIdleConnections()
	response, err := httpClient.Head(url)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	response, err = httpClient.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	download, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || !bytes.Equal(content, download) {
		t.Fatalf("download mismatch: status=%d err=%v", response.StatusCode, err)
	}
	if output := command("list"); !strings.Contains(output, "download.bin") || !strings.Contains(output, "0/1") {
		t.Fatalf("CLI list output: %s", output)
	}
	command("status")
	command("revoke", shares[0].Token)
	remaining, err := client.List(ctx)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("revoked shares: %v: %v", remaining, err)
	}
}
