package ipc_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"shareto/internal/config"
	"shareto/internal/ipc"
	"shareto/internal/store"
)

func TestIPCCommunication(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "shareto-ipc-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	sockPath := filepath.Join(tempDir, "ipc.sock")
	statePath := filepath.Join(tempDir, "state.json")

	// Create a dummy file to share
	sampleFile := filepath.Join(tempDir, "sample.txt")
	if err := os.WriteFile(sampleFile, []byte("Hello Shareto IPC"), 0644); err != nil {
		t.Fatalf("failed to create sample file: %v", err)
	}

	cfg := config.DefaultConfig()
	storeMgr, err := store.NewManager(cfg, statePath)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	ipcSrv, err := ipc.NewServer(cfg, storeMgr, sockPath)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	go func() {
		_ = ipcSrv.Start()
	}()

	time.Sleep(100 * time.Millisecond)

	client := ipc.NewClient(sockPath)
	if !client.Ping() {
		t.Fatalf("IPC client Ping failed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. Share
	shareRes, err := client.Share(ctx, ipc.ShareRequest{
		FilePath:     sampleFile,
		ExpiresSec:   3600,
		MaxDownloads: 5,
	})
	if err != nil {
		t.Fatalf("client.Share failed: %v", err)
	}
	if shareRes.Token == "" {
		t.Fatalf("expected non-empty token")
	}

	// 2. List
	listRes, err := client.List(ctx)
	if err != nil {
		t.Fatalf("client.List failed: %v", err)
	}
	if len(listRes) != 1 || listRes[0].Token != shareRes.Token {
		t.Fatalf("expected 1 share with token %s, got %v", shareRes.Token, listRes)
	}

	// 3. Status
	statusRes, err := client.Status(ctx)
	if err != nil {
		t.Fatalf("client.Status failed: %v", err)
	}
	if statusRes.ActiveSharesCount != 1 {
		t.Fatalf("expected 1 active share, got %d", statusRes.ActiveSharesCount)
	}

	// 4. Revoke
	revokeRes, err := client.Revoke(ctx, shareRes.Token[:8]) // prefix revoke
	if err != nil {
		t.Fatalf("client.Revoke failed: %v", err)
	}
	if len(revokeRes.RevokedTokens) != 1 {
		t.Fatalf("expected 1 revoked token, got %v", revokeRes.RevokedTokens)
	}

	// 5. Verify list is empty
	listAfter, err := client.List(ctx)
	if err != nil {
		t.Fatalf("client.List after revoke failed: %v", err)
	}
	if len(listAfter) != 0 {
		t.Fatalf("expected 0 shares after revoke, got %d", len(listAfter))
	}

	_ = ipcSrv.Stop(ctx)
}
