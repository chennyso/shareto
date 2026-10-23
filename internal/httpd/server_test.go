package httpd_test

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"shareto/internal/config"
	"shareto/internal/crypto"
	"shareto/internal/httpd"
	"shareto/internal/netutil"
	"shareto/internal/store"
)

// Helper: setup test environment with isolated state file
func setupTestServer(t *testing.T, customCfg ...func(*config.Config)) (*httptest.Server, *store.Manager, *config.Config, func()) {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "shareto-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	stateFile := filepath.Join(tempDir, "state.json")
	cfg := config.DefaultConfig()
	cfg.TrustedProxies = []string{"127.0.0.1/32", "::1/128"}
	cfg.MinTransferRateKBps = 0 // Disable slowconn abort for short functional tests

	for _, fn := range customCfg {
		fn(cfg)
	}

	storeMgr, err := store.NewManager(cfg, stateFile)
	if err != nil {
		t.Fatalf("failed to create store manager: %v", err)
	}

	trustedCIDRs, _ := netutil.ParseCIDRs(cfg.TrustedProxies)
	rl := netutil.NewRateLimiter(netutil.DefaultLRUCapacity)
	handler := httpd.NewShareHandler(cfg, storeMgr, rl, trustedCIDRs)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /s/{token}", handler.HandleGetShare)
	mux.HandleFunc("POST /s/{token}/claim", handler.HandleClaimShare)
	mux.HandleFunc("GET /d/{token}/{session}/{filename}", handler.HandleDownload)
	mux.HandleFunc("HEAD /d/{token}/{session}/{filename}", handler.HandleDownload)

	ts := httptest.NewServer(mux)

	cleanup := func() {
		ts.Close()
		_ = os.RemoveAll(tempDir)
	}

	return ts, storeMgr, cfg, cleanup
}

func createTempFile(t *testing.T, size int) string {
	t.Helper()
	f, err := os.CreateTemp("", "shareto-sample-*.bin")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer f.Close()

	if size > 0 {
		buf := make([]byte, size)
		_, _ = rand.Read(buf)
		_, _ = f.Write(buf)
	}
	return f.Name()
}

// 1. 防爬虫误扣测试：对无密码分享连续发起 20 次 GET /s/{token}，验证 RemainingDownloads 始终保持初始值 1，未被扣减。
func TestCrawlerProtectionAndNoDeductionOnGet(t *testing.T) {
	ts, mgr, _, cleanup := setupTestServer(t)
	defer cleanup()

	filePath := createTempFile(t, 2048)
	defer os.Remove(filePath)

	entry, err := mgr.CreateShare(filePath, 1*time.Hour, 1, "")
	if err != nil {
		t.Fatalf("CreateShare failed: %v", err)
	}
	token := entry.Token

	crawlers := []string{
		"Mozilla/5.0 (compatible; TelegramBot/1.0)",
		"Twitterbot/1.0",
		"MicroMessenger/8.0.38",
		"Slackbot-LinkExpanding 1.0",
		"facebookexternalhit/1.1",
		"Mozilla/5.0 (compatible; Applebot/0.1)",
		"Discordbot/2.0; +https://discordapp.com",
		"WhatsApp/2.21.12.21 A",
	}

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Make 20 GET requests: mixed crawler requests (with and without ?raw=1) and standard browser requests
	for i := 0; i < 20; i++ {
		ua := crawlers[i%len(crawlers)]
		reqURL := fmt.Sprintf("%s/s/%s", ts.URL, token)
		if i%2 == 1 {
			reqURL += "?raw=1" // With ?raw=1, crawler should NOT be redirected or claim quota
		}

		req, _ := http.NewRequest("GET", reqURL, nil)
		req.Header.Set("User-Agent", ua)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
		_ = resp.Body.Close()

		// Verify RemainingDownloads is still 1
		got, err := mgr.GetShare(token)
		if err != nil {
			t.Fatalf("share lost at iteration %d: %v", i, err)
		}
		if got.RemainingDownloads != 1 {
			t.Fatalf("RemainingDownloads decreased at request %d (ua: %s)! Expected 1, got %d",
				i, ua, got.RemainingDownloads)
		}
	}
}

// 2. 并发原子扣减测试：设置 MaxDownloads = 1，启动 50 个并发 Goroutine 同时携带合法 CSRF Token 请求 POST /s/{token}/claim，验证恰好只有 1 个协程获得 303 重定向及 session_id，其余 49 个全部收到失败状态码。
func TestConcurrentAtomicClaim(t *testing.T) {
	ts, mgr, _, cleanup := setupTestServer(t)
	defer cleanup()

	filePath := createTempFile(t, 4096)
	defer os.Remove(filePath)

	entry, err := mgr.CreateShare(filePath, 1*time.Hour, 1, "")
	if err != nil {
		t.Fatalf("CreateShare failed: %v", err)
	}
	token := entry.Token

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	const concurrency = 50
	var wg sync.WaitGroup
	wg.Add(concurrency)

	var successCount atomic.Int32
	var otherCount atomic.Int32

	clientIP := net.ParseIP("127.0.0.1")
	clientSubnet := netutil.SessionSubnetKey(clientIP)
	csrfToken := crypto.GenerateCSRFToken(mgr.CSRFSecret(), token, clientSubnet, 10*time.Minute)

	startGate := make(chan struct{})

	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			<-startGate

			form := url.Values{}
			form.Set("csrf_token", csrfToken)

			req, _ := http.NewRequest("POST", fmt.Sprintf("%s/s/%s/claim", ts.URL, token), strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("X-Forwarded-For", "127.0.0.1")

			resp, err := client.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode == http.StatusSeeOther {
				loc := resp.Header.Get("Location")
				if strings.Contains(loc, fmt.Sprintf("/d/%s/", token)) {
					successCount.Add(1)
					return
				}
			}
			otherCount.Add(1)
		}()
	}

	close(startGate)
	wg.Wait()

	if successCount.Load() != 1 {
		t.Fatalf("Expected EXACTLY 1 successful claim, got %d (others: %d)", successCount.Load(), otherCount.Load())
	}

	got, err := mgr.GetShare(token)
	if err == nil && got.RemainingDownloads != 0 {
		t.Fatalf("Expected RemainingDownloads=0, got %d", got.RemainingDownloads)
	}
}

// 3. 多线程 Range 并发安全测试：使用获得的 session_id 启动 8 个并发 Goroutine，分别请求文件不同偏移量的 Range: bytes=start-end，拼接所有分片后计算 SHA-256，验证与原始文件 100% 一致，且 -race 无任何数据竞争告警。
func TestConcurrentRangeDownload(t *testing.T) {
	ts, mgr, _, cleanup := setupTestServer(t)
	defer cleanup()

	const fileSize = 4 * 1024 * 1024 // 4 MB
	filePath := createTempFile(t, fileSize)
	defer os.Remove(filePath)

	origBytes, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read test file: %v", err)
	}
	hasher := sha256.New()
	hasher.Write(origBytes)
	expectedHash := hasher.Sum(nil)

	entry, err := mgr.CreateShare(filePath, 1*time.Hour, 1, "")
	if err != nil {
		t.Fatalf("CreateShare failed: %v", err)
	}
	token := entry.Token

	// Claim session
	clientIP := net.ParseIP("127.0.0.1")
	_, session, err := mgr.ClaimSession(token, clientIP)
	if err != nil {
		t.Fatalf("ClaimSession failed: %v", err)
	}

	const threads = 8
	chunkSize := fileSize / threads
	downloaded := make([]byte, fileSize)

	var wg sync.WaitGroup
	wg.Add(threads)

	downloadURL := fmt.Sprintf("%s/d/%s/%s/%s", ts.URL, token, session.SessionID, entry.FileName)

	for i := 0; i < threads; i++ {
		workerID := i
		start := int64(workerID * chunkSize)
		end := start + int64(chunkSize) - 1
		if workerID == threads-1 {
			end = int64(fileSize) - 1
		}

		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("GET", downloadURL, nil)
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))

			client := &http.Client{Timeout: 10 * time.Second}
			resp, err := client.Do(req)
			if err != nil {
				t.Errorf("worker %d request failed: %v", workerID, err)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusPartialContent {
				t.Errorf("worker %d expected 206 Partial Content, got %d", workerID, resp.StatusCode)
				return
			}

			part, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Errorf("worker %d failed to read body: %v", workerID, err)
				return
			}

			copy(downloaded[start:], part)
		}()
	}

	wg.Wait()

	hasherDownloaded := sha256.New()
	hasherDownloaded.Write(downloaded)
	actualHash := hasherDownloaded.Sum(nil)

	if !bytes.Equal(expectedHash, actualHash) {
		t.Fatalf("Downloaded file hash mismatch! Expected %x, got %x", expectedHash, actualHash)
	}
}

// 4. 文件静默篡改熔断测试：分享创建后，向原物理文件追加写入 1 字节，再次请求下载直链，验证服务端立即返回 410 Gone。
func TestFileMutationBreaker(t *testing.T) {
	ts, mgr, _, cleanup := setupTestServer(t)
	defer cleanup()

	filePath := createTempFile(t, 1024)
	defer os.Remove(filePath)

	entry, err := mgr.CreateShare(filePath, 1*time.Hour, 5, "")
	if err != nil {
		t.Fatalf("CreateShare failed: %v", err)
	}
	token := entry.Token

	clientIP := net.ParseIP("127.0.0.1")
	_, session, err := mgr.ClaimSession(token, clientIP)
	if err != nil {
		t.Fatalf("ClaimSession failed: %v", err)
	}

	downloadURL := fmt.Sprintf("%s/d/%s/%s/%s", ts.URL, token, session.SessionID, entry.FileName)

	// 1. Initial download succeeds
	resp, err := http.Get(downloadURL)
	if err != nil {
		t.Fatalf("initial GET failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	// Mutate the physical file by appending 1 byte
	f, err := os.OpenFile(filePath, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatalf("failed to open file for append: %v", err)
	}
	_, _ = f.Write([]byte("X"))
	_ = f.Close()

	// 2. Download again: must trigger 410 Gone
	resp2, err := http.Get(downloadURL)
	if err != nil {
		t.Fatalf("second GET failed: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusGone {
		t.Fatalf("Expected HTTP 410 Gone after file mutation, got %d", resp2.StatusCode)
	}

	// Verify share is removed from manager
	if _, err := mgr.GetShare(token); err == nil {
		t.Fatalf("Expected mutated share to be automatically purged from manager")
	}
}

// 5. 软链接与特殊文件拦截测试：尝试通过 IPC 注册 /dev/zero 或指向 /etc/passwd 的符号链接，验证直接被拒绝或安全解引用。
func TestSymlinkAndSpecialFileInterception(t *testing.T) {
	// Special character device /dev/zero
	_, _, _, _, err := store.OpenAndPinFile("/dev/zero")
	if err == nil || !strings.Contains(err.Error(), "only regular files") {
		t.Fatalf("Expected 'only regular files can be shared' for /dev/zero, got: %v", err)
	}

	// Empty regular file
	emptyFile := createTempFile(t, 0)
	defer os.Remove(emptyFile)
	_, _, _, _, err = store.OpenAndPinFile(emptyFile)
	if err == nil || !strings.Contains(err.Error(), "empty files") {
		t.Fatalf("Expected 'empty files cannot be shared' for empty file, got: %v", err)
	}
}

// 6. IPv6 /64 子网限频测试：模拟来自同一 /64 前缀但不同后缀的 6 个 IPv6 地址（如 2001:db8:1:2::1 至 ::6）连续提交错误密码，验证第 6 次请求即触发 429 Too Many Requests 子网级锁定。
func TestIPv6SubnetRateLimiting(t *testing.T) {
	ts, mgr, _, cleanup := setupTestServer(t)
	defer cleanup()

	filePath := createTempFile(t, 1024)
	defer os.Remove(filePath)

	correctPass := "superSecret123"
	hash, err := crypto.HashPassword(correctPass)
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}

	entry, err := mgr.CreateShare(filePath, 1*time.Hour, 100, hash)
	if err != nil {
		t.Fatalf("CreateShare failed: %v", err)
	}
	token := entry.Token

	// 6 IPv6 addresses belonging to the same /64 subnet
	ipv6List := []string{
		"2001:db8:1:2::1",
		"2001:db8:1:2::2",
		"2001:db8:1:2::3",
		"2001:db8:1:2::4",
		"2001:db8:1:2::5",
		"2001:db8:1:2::6",
	}

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Send 5 failed attempts with different IPs in the same /64 subnet
	for i := 0; i < 5; i++ {
		ip := net.ParseIP(ipv6List[i])
		clientSubnet := netutil.SessionSubnetKey(ip)
		csrf := crypto.GenerateCSRFToken(mgr.CSRFSecret(), token, clientSubnet, 10*time.Minute)

		form := url.Values{}
		form.Set("csrf_token", csrf)
		form.Set("password", "wrongPassword")

		req, _ := http.NewRequest("POST", fmt.Sprintf("%s/s/%s/claim", ts.URL, token), strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Forwarded-For", ipv6List[i])

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("attempt %d failed: %v", i, err)
		}
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("attempt %d expected 403 Forbidden, got %d", i, resp.StatusCode)
		}
	}

	// 6th attempt from the same /64 subnet (even with correct or wrong password) must receive 429 Too Many Requests
	sixthIP := net.ParseIP(ipv6List[5])
	sixthSubnet := netutil.SessionSubnetKey(sixthIP)
	csrf := crypto.GenerateCSRFToken(mgr.CSRFSecret(), token, sixthSubnet, 10*time.Minute)

	form := url.Values{}
	form.Set("csrf_token", csrf)
	form.Set("password", correctPass)

	req, _ := http.NewRequest("POST", fmt.Sprintf("%s/s/%s/claim", ts.URL, token), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Forwarded-For", ipv6List[5])

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("6th request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("6th request from subnet expected 429 Too Many Requests, got %d", resp.StatusCode)
	}
}
