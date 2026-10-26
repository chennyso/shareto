package store_test

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"shareto/internal/config"
	"shareto/internal/store"
)

func testManager(t *testing.T) (*store.Manager, *config.Config, string, string) {
	t.Helper()
	dir := t.TempDir()
	state := filepath.Join(dir, "state", "state.json")
	cfg := config.DefaultConfig()
	mgr, err := store.NewManager(cfg, state)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "content.bin")
	if err := os.WriteFile(path, []byte("original file content"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, entry := range mgr.ListShares() {
			entry.Close()
		}
	})
	return mgr, cfg, state, path
}

func TestGlobalAuthenticationCapacity(t *testing.T) {
	mgr, _, _, path := testManager(t)
	var entries []*store.ShareEntry
	for i := 0; i < 3; i++ {
		entry, err := mgr.CreateShare(path, time.Hour, 3, "")
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
	}
	first, err := mgr.AcquireAuthSlots(entries[0])
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	second, err := mgr.AcquireAuthSlots(entries[1])
	if err != nil {
		t.Fatal(err)
	}
	defer second()
	if release, err := mgr.AcquireAuthSlots(entries[2]); !errors.Is(err, store.ErrConcurrencyLimit) || release != nil {
		t.Fatalf("global authentication limit was exceeded: %v", err)
	}
	first()
	first()
	third, err := mgr.AcquireAuthSlots(entries[2])
	if err != nil {
		t.Fatal(err)
	}
	third()
}

func TestRevocationReportsPersistenceFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission failure requires an unprivileged user")
	}
	mgr, _, state, path := testManager(t)
	entry, err := mgr.CreateShare(path, time.Hour, 3, "")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(state)
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	if _, err := mgr.RevokeShare(entry.Token); err == nil {
		t.Fatal("revocation reported success after a real persistence failure")
	}
	if _, err := mgr.GetShare(entry.Token); !errors.Is(err, store.ErrShareNotFound) {
		t.Fatalf("failed persistence restored revoked in-memory access: %v", err)
	}
}

func TestCommittedLastSessionSurvivesRestart(t *testing.T) {
	mgr, cfg, state, path := testManager(t)
	entry, err := mgr.CreateShare(path, time.Hour, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	client := net.ParseIP("192.0.2.1")
	_, session, err := mgr.ClaimSession(entry.Token, client)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := store.NewManager(cfg, state)
	if err != nil {
		t.Fatal(err)
	}
	restored, _, err := reloaded.GetSession(entry.Token, session.SessionID, client)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if restored.Remaining() != 0 {
		t.Fatal("committed quota was restored")
	}
	if _, _, err := reloaded.ClaimSession(entry.Token, client); !errors.Is(err, store.ErrDownloadExhausted) {
		t.Fatalf("exhausted share authorized another session: %v", err)
	}
	if _, err := mgr.RevokeShare(entry.Token); err != nil {
		t.Fatal(err)
	}
	revoked, err := store.NewManager(cfg, state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := revoked.GetShare(entry.Token); !errors.Is(err, store.ErrShareNotFound) {
		t.Fatalf("committed revocation was lost: %v", err)
	}
}

func TestMutationWithRestoredMtimeIsRejected(t *testing.T) {
	mgr, _, _, path := testManager(t)
	entry, err := mgr.CreateShare(path, time.Hour, 3, "")
	if err != nil {
		t.Fatal(err)
	}
	reader, originalMtime, err := store.GetSectionReader(entry)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("changed!"), 0); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()
	if err := os.Chtimes(path, originalMtime, originalMtime); err != nil {
		t.Fatal(err)
	}
	if count, err := reader.Read(make([]byte, 16)); count != 0 || !errors.Is(err, store.ErrFileMutated) {
		t.Fatalf("reader exposed changed bytes: count=%d err=%v", count, err)
	}
}

func TestInvalidStateFailsInitialization(t *testing.T) {
	_, cfg, state, _ := testManager(t)
	if err := os.MkdirAll(filepath.Dir(state), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, []byte("invalid JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.NewManager(cfg, state); err == nil {
		t.Fatal("corrupt state was silently discarded")
	}
}
