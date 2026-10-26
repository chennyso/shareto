package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"shareto/internal/config"
	"shareto/internal/store"

	"golang.org/x/sys/unix"
)

type Server struct {
	cfg       *config.Config
	storeMgr  *store.Manager
	listener  net.Listener
	httpSrv   *http.Server
	sockPath  string
	startTime time.Time
	running   atomic.Bool
}

type peerCredListener struct {
	net.Listener
	expectedUID uint32
}

func (l *peerCredListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}

		unixConn, ok := conn.(*net.UnixConn)
		if !ok {
			_ = conn.Close()
			continue
		}

		raw, err := unixConn.SyscallConn()
		if err != nil {
			_ = conn.Close()
			continue
		}

		var cred *unix.Ucred
		var credErr error
		rawErr := raw.Control(func(fd uintptr) {
			cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		})

		if rawErr != nil || credErr != nil || cred == nil || cred.Uid != l.expectedUID {
			_ = conn.Close()
			continue
		}

		return conn, nil
	}
}

// DefaultSocketPath returns the standard Unix socket path.
func DefaultSocketPath() string {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		uid := os.Getuid()
		runUser := fmt.Sprintf("/run/user/%d", uid)
		if fi, err := os.Stat(runUser); err == nil && fi.IsDir() {
			runtimeDir = runUser
		} else {
			runtimeDir = fmt.Sprintf("/tmp/shareto-%d", uid)
		}
	}
	return filepath.Join(runtimeDir, "shareto", "ipc.sock")
}

// NewServer initializes the IPC server.
func NewServer(cfg *config.Config, storeMgr *store.Manager, customSockPath ...string) (*Server, error) {
	sockPath := DefaultSocketPath()
	if len(customSockPath) > 0 && customSockPath[0] != "" {
		sockPath = customSockPath[0]
	}

	return &Server{
		cfg:       cfg,
		storeMgr:  storeMgr,
		sockPath:  sockPath,
		startTime: time.Now(),
	}, nil
}

// Start begins listening on the Unix Domain Socket with SO_PEERCRED checks.
func (s *Server) Start() error {
	sockDir := filepath.Dir(s.sockPath)
	if err := os.MkdirAll(sockDir, 0700); err != nil {
		return fmt.Errorf("failed to create socket directory: %w", err)
	}

	// Probe old socket
	if _, err := os.Stat(s.sockPath); err == nil {
		c, err := net.DialTimeout("unix", s.sockPath, 100*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return fmt.Errorf("another daemon instance is already running on %s", s.sockPath)
		}
		_ = os.Remove(s.sockPath)
	}

	rawListener, err := net.Listen("unix", s.sockPath)
	if err != nil {
		return fmt.Errorf("failed to listen on unix socket: %w", err)
	}

	if err := os.Chmod(s.sockPath, 0600); err != nil {
		_ = rawListener.Close()
		return fmt.Errorf("failed to set socket permissions: %w", err)
	}

	s.listener = &peerCredListener{
		Listener:    rawListener,
		expectedUID: uint32(os.Getuid()),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /ipc/share", s.handleShare)
	mux.HandleFunc("GET /ipc/list", s.handleList)
	mux.HandleFunc("POST /ipc/revoke", s.handleRevoke)
	mux.HandleFunc("GET /ipc/status", s.handleStatus)

	s.httpSrv = &http.Server{
		Handler:      http.MaxBytesHandler(mux, 64*1024),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	s.running.Store(true)
	return s.httpSrv.Serve(s.listener)
}

// Stop gracefully shuts down the IPC server and cleans up the socket.
func (s *Server) Stop(ctx context.Context) error {
	s.running.Store(false)
	var err error
	if s.httpSrv != nil {
		err = s.httpSrv.Shutdown(ctx)
	}
	if s.listener != nil {
		_ = s.listener.Close()
	}
	_ = os.Remove(s.sockPath)
	return err
}

// Request and response payloads
type ShareRequest struct {
	FilePath      string `json:"file_path"`
	ExpiresSec    int64  `json:"expires_sec"`
	MaxDownloads  int    `json:"max_downloads"`
	PasswordHash  string `json:"password_hash,omitempty"`
	PublicBaseURL string `json:"public_base_url,omitempty"`
}

type ShareResponse struct {
	Token     string `json:"token"`
	URL       string `json:"url"`
	RawURL    string `json:"raw_url"`
	ExpiresAt string `json:"expires_at"`
}

type ShareItemResponse struct {
	Token              string `json:"token"`
	FileName           string `json:"file_name"`
	FilePath           string `json:"file_path"`
	FileSize           int64  `json:"file_size"`
	RemainingDownloads int    `json:"remaining_downloads"`
	MaxDownloads       int    `json:"max_downloads"`
	ActiveStreams      int32  `json:"active_streams"`
	HasPassword        bool   `json:"has_password"`
	CreatedAt          string `json:"created_at"`
	ExpiresAt          string `json:"expires_at"`
}

type RevokeRequest struct {
	Token string `json:"token"`
}

type RevokeResponse struct {
	RevokedTokens []string `json:"revoked_tokens"`
}

type StatusResponse struct {
	PID               int    `json:"pid"`
	UptimeSec         int64  `json:"uptime_sec"`
	GlobalActiveConns int32  `json:"global_active_conns"`
	ActiveSharesCount int    `json:"active_shares_count"`
	Version           string `json:"version"`
}

func (s *Server) handleShare(w http.ResponseWriter, r *http.Request) {
	var req ShareRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"invalid json: %s"}`, err.Error()), http.StatusBadRequest)
		return
	}

	dur := time.Duration(req.ExpiresSec) * time.Second
	entry, err := s.storeMgr.CreateShare(req.FilePath, dur, req.MaxDownloads, req.PasswordHash)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadRequest)
		return
	}

	base := s.cfg.PublicBaseURL
	if req.PublicBaseURL != "" {
		base = strings.TrimRight(req.PublicBaseURL, "/")
	}
	resp := ShareResponse{
		Token:     entry.Token,
		URL:       fmt.Sprintf("%s/s/%s", base, entry.Token),
		RawURL:    fmt.Sprintf("%s/s/%s?raw=1", base, entry.Token),
		ExpiresAt: entry.ExpiresAt.Format(time.RFC3339),
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	shares := s.storeMgr.ListShares()
	resp := make([]ShareItemResponse, 0, len(shares))

	for _, entry := range shares {
		resp = append(resp, ShareItemResponse{
			Token:              entry.Token,
			FileName:           entry.FileName,
			FilePath:           entry.FilePath,
			FileSize:           entry.FileSize,
			RemainingDownloads: entry.Remaining(),
			MaxDownloads:       entry.MaxDownloads,
			ActiveStreams:      entry.ActiveConns.Load(),
			HasPassword:        entry.PasswordHash != "",
			CreatedAt:          entry.CreatedAt.Format(time.RFC3339),
			ExpiresAt:          entry.ExpiresAt.Format(time.RFC3339),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	var req RevokeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"invalid json: %s"}`, err.Error()), http.StatusBadRequest)
		return
	}

	revoked, err := s.storeMgr.RevokeShare(req.Token)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	resp := RevokeResponse{
		RevokedTokens: revoked,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	resp := StatusResponse{
		PID:               os.Getpid(),
		UptimeSec:         int64(time.Since(s.startTime).Seconds()),
		GlobalActiveConns: s.storeMgr.GlobalActiveConns(),
		ActiveSharesCount: len(s.storeMgr.ListShares()),
		Version:           "3.0.0",
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
