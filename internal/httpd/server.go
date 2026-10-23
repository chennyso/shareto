package httpd

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"shareto/internal/config"
	"shareto/internal/netutil"
	"shareto/internal/store"
)

type Server struct {
	cfg         *config.Config
	storeMgr    *store.Manager
	rateLimiter *netutil.RateLimiter
	httpServer  *http.Server
	listener    net.Listener
}

func NewServer(cfg *config.Config, storeMgr *store.Manager) (*Server, error) {
	trustedCIDRs, err := netutil.ParseCIDRs(cfg.TrustedProxies)
	if err != nil {
		return nil, fmt.Errorf("failed to parse trusted proxies: %w", err)
	}

	rateLimiter := netutil.NewRateLimiter(netutil.DefaultLRUCapacity)
	handler := NewShareHandler(cfg, storeMgr, rateLimiter, trustedCIDRs)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /s/{token}", handler.HandleGetShare)
	mux.HandleFunc("POST /s/{token}/claim", handler.HandleClaimShare)
	mux.HandleFunc("GET /d/{token}/{session}/{filename}", handler.HandleDownload)
	mux.HandleFunc("HEAD /d/{token}/{session}/{filename}", handler.HandleDownload)

	// Global security header middleware
	secureHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		mux.ServeHTTP(w, r)
	})

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           secureHandler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	return &Server{
		cfg:         cfg,
		storeMgr:    storeMgr,
		rateLimiter: rateLimiter,
		httpServer:  srv,
	}, nil
}

func expandPath(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// Start listens and serves HTTP/HTTPS traffic.
func (s *Server) Start() error {
	var ln net.Listener
	var err error

	if s.cfg.TLS.Enabled {
		if s.cfg.TLS.CertFile == "" || s.cfg.TLS.KeyFile == "" {
			return fmt.Errorf("TLS is enabled but cert_file or key_file is missing")
		}
		certPath := expandPath(s.cfg.TLS.CertFile)
		keyPath := expandPath(s.cfg.TLS.KeyFile)
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return fmt.Errorf("failed to load TLS certificate: %w", err)
		}
		tlsConfig := &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
		ln, err = tls.Listen("tcp", s.cfg.ListenAddr, tlsConfig)
		if err != nil {
			return fmt.Errorf("failed to listen on %s: %w", s.cfg.ListenAddr, err)
		}
	} else {
		ln, err = net.Listen("tcp", s.cfg.ListenAddr)
		if err != nil {
			return fmt.Errorf("failed to listen on %s: %w", s.cfg.ListenAddr, err)
		}
	}

	s.listener = ln
	return s.httpServer.Serve(ln)
}

// Stop gracefully shuts down the HTTP server.
func (s *Server) Stop(ctx context.Context) error {
	if s.httpServer != nil {
		return s.httpServer.Shutdown(ctx)
	}
	return nil
}

// Addr returns the bound network address.
func (s *Server) Addr() net.Addr {
	if s.listener != nil {
		return s.listener.Addr()
	}
	return nil
}
