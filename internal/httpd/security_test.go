package httpd_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"shareto/internal/config"
	"shareto/internal/crypto"
	"shareto/internal/httpd"
	"shareto/internal/netutil"
	"shareto/internal/store"
)

func startSecurityServer(t *testing.T) (string, *store.Manager, *http.Client, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.ListenAddr = "127.0.0.1:0"
	state := filepath.Join(dir, "state", "state.json")
	mgr, err := store.NewManager(cfg, state)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := httpd.NewServer(cfg, mgr)
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- srv.Start() }()
	deadline := time.Now().Add(5 * time.Second)
	for srv.Addr() == nil {
		select {
		case err := <-stopped:
			t.Fatal(err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("HTTP server startup timed out")
		}
		time.Sleep(time.Millisecond)
	}
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	t.Cleanup(func() {
		client.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Stop(ctx); err != nil {
			t.Error(err)
		}
		if err := <-stopped; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Error(err)
		}
		for _, entry := range mgr.ListShares() {
			if err := mgr.RemoveShare(entry.Token); err != nil {
				t.Error(err)
			}
		}
	})
	return "http://" + srv.Addr().String(), mgr, client, state
}

func securityShare(t *testing.T, mgr *store.Manager, quota int) *store.ShareEntry {
	t.Helper()
	path := filepath.Join(t.TempDir(), "payload.bin")
	if err := os.WriteFile(path, []byte("shareto regression content"), 0600); err != nil {
		t.Fatal(err)
	}
	entry, err := mgr.CreateShare(path, time.Hour, quota, "")
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func TestRequestBodyReadDeadline(t *testing.T) {
	base, _, _, _ := startSecurityServer(t)
	conn, err := net.Dial("tcp", base[len("http://"):])
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(7 * time.Second)); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := fmt.Fprint(conn, "POST /s/unknown/claim HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/x-www-form-urlencoded\r\nContent-Length: 64\r\n\r\nx="); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest || time.Since(started) > 6500*time.Millisecond {
		t.Fatalf("incomplete request body: status=%d elapsed=%s", response.StatusCode, time.Since(started))
	}
}

func TestHEADPreservesRawShareQuota(t *testing.T) {
	base, mgr, client, _ := startSecurityServer(t)
	entry := securityShare(t, mgr, 1)
	for _, suffix := range []string{"?raw=1", ""} {
		request, err := http.NewRequest(http.MethodHead, base+"/s/"+entry.Token+suffix, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("X-Shareto-Direct", "1")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK || entry.Remaining() != 1 || response.Header.Get("Location") != "" {
			t.Fatalf("HEAD changed authorization: status=%d remaining=%d", response.StatusCode, entry.Remaining())
		}
	}
	response, err := client.Get(base + "/s/" + entry.Token + "?raw=1")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusTemporaryRedirect || entry.Remaining() != 0 {
		t.Fatalf("GET authorization: status=%d remaining=%d", response.StatusCode, entry.Remaining())
	}
}

func TestChangedContentAbortsActiveDownload(t *testing.T) {
	base, mgr, client, _ := startSecurityServer(t)
	path := filepath.Join(t.TempDir(), "changing.bin")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := file.Truncate(64 << 20); err != nil {
		t.Fatal(err)
	}
	entry, err := mgr.CreateShare(path, time.Hour, 3, "")
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(base + "/s/" + entry.Token + "?raw=1")
	if err != nil {
		t.Fatal(err)
	}
	location := response.Header.Get("Location")
	response.Body.Close()
	response, err = client.Get(base + location)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if _, err := io.CopyN(io.Discard, response.Body, 1<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt(bytes.Repeat([]byte("S"), 64<<10), 48<<20); err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(response.Body)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("modified download must terminate early: %v", err)
	}
	if bytes.Contains(content, []byte("S")) {
		t.Fatal("download included modified content")
	}
	if _, err := mgr.GetShare(entry.Token); !errors.Is(err, store.ErrShareNotFound) {
		t.Fatalf("mutated share remains available: %v", err)
	}
}

func TestAuthorizationReportsPersistenceFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission failure requires an unprivileged user")
	}
	base, mgr, client, state := startSecurityServer(t)
	entry := securityShare(t, mgr, 3)
	dir := filepath.Dir(state)
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	response, err := client.Get(base + "/s/" + entry.Token + "?raw=1")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable || response.Header.Get("Location") != "" || entry.Remaining() != 3 {
		t.Fatalf("failed persistence issued authorization: status=%d remaining=%d", response.StatusCode, entry.Remaining())
	}
	entry.Mu.Lock()
	sessions := len(entry.Sessions)
	entry.Mu.Unlock()
	if sessions != 0 {
		t.Fatalf("failed authorization retained %d sessions", sessions)
	}
}

func TestConcurrentPasswordClaimsRemainBounded(t *testing.T) {
	server, mgr, _, cleanup := setupTestServer(t)
	defer cleanup()
	hash, err := crypto.HashPassword("correct-password")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "protected.bin")
	if err := os.WriteFile(path, []byte("protected content"), 0600); err != nil {
		t.Fatal(err)
	}
	entry, err := mgr.CreateShare(path, time.Hour, 10, hash)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	defer client.CloseIdleConnections()
	csrf := crypto.GenerateCSRFToken(mgr.CSRFSecret(), entry.Token, netutil.SessionSubnetKey(net.ParseIP("127.0.0.1")), 10*time.Minute)
	claim := func(token, csrf, password string) int {
		response, err := client.PostForm(server.URL+"/s/"+token+"/claim", url.Values{"csrf_token": {csrf}, "password": {password}})
		if err != nil {
			t.Error(err)
			return 0
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		return response.StatusCode
	}
	gate := make(chan struct{})
	statuses := make(chan int, 24)
	var workers sync.WaitGroup
	for i := 0; i < 24; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-gate
			statuses <- claim(entry.Token, csrf, "wrong-password")
		}()
	}
	close(gate)
	workers.Wait()
	close(statuses)
	failures := 0
	for status := range statuses {
		switch status {
		case http.StatusForbidden:
			failures++
		case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		default:
			t.Fatalf("unexpected password response: %d", status)
		}
	}
	if failures > netutil.MaxFailures {
		t.Fatalf("concurrent password verification exceeded limit: %d", failures)
	}
	for failures < netutil.MaxFailures {
		if status := claim(entry.Token, csrf, "wrong-password"); status != http.StatusForbidden {
			t.Fatalf("password failure before lockout: %d", status)
		}
		failures++
	}
	if status := claim(entry.Token, csrf, "correct-password"); status != http.StatusTooManyRequests {
		t.Fatalf("locked share accepted password verification: %d", status)
	}
	fresh, err := mgr.CreateShare(path, time.Hour, 1, hash)
	if err != nil {
		t.Fatal(err)
	}
	freshCSRF := crypto.GenerateCSRFToken(mgr.CSRFSecret(), fresh.Token, netutil.SessionSubnetKey(net.ParseIP("127.0.0.1")), 10*time.Minute)
	if status := claim(fresh.Token, freshCSRF, "correct-password"); status != http.StatusSeeOther || fresh.Remaining() != 0 {
		t.Fatalf("correct password did not authorize a session: status=%d remaining=%d", status, fresh.Remaining())
	}
}
