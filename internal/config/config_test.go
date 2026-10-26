package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"shareto/internal/config"
)

func TestLegacyConfigReceivesSecurityDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"listen_addr":"127.0.0.1:38080"}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadOrInit(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConnections != 256 || cfg.MaxConnectionsPerIP != 32 || cfg.MaxConcurrentAuth != 2 {
		t.Fatalf("unexpected security defaults: %+v", cfg)
	}
}

func TestInvalidSecurityLimitsAreRejected(t *testing.T) {
	for _, invalid := range []string{
		`{"max_connections":0}`,
		`{"max_connections_per_ip":257}`,
		`{"max_concurrent_auth":0}`,
		`{"max_concurrent_auth":17}`,
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := config.LoadOrInit(path); err == nil {
			t.Fatalf("invalid configuration accepted: %s", invalid)
		}
	}
}
