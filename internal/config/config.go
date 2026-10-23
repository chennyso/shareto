package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Config struct {
	ListenAddr           string    `json:"listen_addr"`             // 默认 "127.0.0.1:38080"
	PublicBaseURL        string    `json:"public_base_url"`         // 默认 "https://share.example.com"
	TLS                  TLSConfig `json:"tls"`
	TrustedProxies       []string  `json:"trusted_proxies"`         // 默认 ["127.0.0.1/32", "::1/128"]
	MaxGlobalStreams     int       `json:"max_global_streams"`      // 默认 64
	MaxStreamsPerToken   int       `json:"max_streams_per_token"`   // 默认 16
	MaxStreamsPerSession int       `json:"max_streams_per_session"` // 默认 8
	MaxActiveShares      int       `json:"max_active_shares"`       // 默认 100
	MinTransferRateKBps  int       `json:"min_transfer_rate_kbps"`  // 默认 16 (即 16KB/s)
}

type TLSConfig struct {
	Enabled  bool   `json:"enabled"`
	CertFile string `json:"cert_file"`
	KeyFile  string `json:"key_file"`
}

// DefaultConfig returns default configuration parameters.
func DefaultConfig() *Config {
	return &Config{
		ListenAddr:    "127.0.0.1:38080",
		PublicBaseURL: "http://127.0.0.1:38080",
		TLS: TLSConfig{
			Enabled:  false,
			CertFile: "",
			KeyFile:  "",
		},
		TrustedProxies:       []string{"127.0.0.1/32", "::1/128"},
		MaxGlobalStreams:     64,
		MaxStreamsPerToken:   16,
		MaxStreamsPerSession: 8,
		MaxActiveShares:      100,
		MinTransferRateKBps:  16,
	}
}

// DefaultConfigPath returns the path to config.json according to XDG spec.
func DefaultConfigPath() string {
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		configDir = filepath.Join(home, ".config")
	}
	return filepath.Join(configDir, "shareto", "config.json")
}

// LoadOrInit loads the config file from disk. If it does not exist,
// it generates and writes the default config.
func LoadOrInit(customPath ...string) (*Config, error) {
	path := DefaultConfigPath()
	if len(customPath) > 0 && customPath[0] != "" {
		path = customPath[0]
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := DefaultConfig()
			if err := SaveConfig(path, cfg); err != nil {
				return nil, fmt.Errorf("failed to save default config: %w", err)
			}
			return cfg, nil
		}
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	cfg := DefaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config JSON: %w", err)
	}
	return cfg, nil
}

// SaveConfig writes the config to disk with restricted permissions.
func SaveConfig(path string, cfg *Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create config dir: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	tmpPath := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write temp config: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to commit config file: %w", err)
	}
	return nil
}
