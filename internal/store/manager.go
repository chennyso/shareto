package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"shareto/internal/config"
	"shareto/internal/crypto"
	"shareto/internal/netutil"
)

type Manager struct {
	mu                sync.RWMutex
	shares            map[string]*ShareEntry
	cfg               *config.Config
	stateFilePath     string
	globalActiveConns atomic.Int32
	csrfSecret        []byte
	saveMu            sync.Mutex
}

// DefaultStatePath returns the path to state.json under $XDG_STATE_HOME.
func DefaultStatePath() string {
	stateDir := os.Getenv("XDG_STATE_HOME")
	if stateDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		stateDir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(stateDir, "shareto", "state.json")
}

// NewManager initializes the store manager, loads state and pins existing valid files.
func NewManager(cfg *config.Config, customStatePath ...string) (*Manager, error) {
	statePath := DefaultStatePath()
	if len(customStatePath) > 0 && customStatePath[0] != "" {
		statePath = customStatePath[0]
	}

	secret, err := crypto.GenerateSecretKey()
	if err != nil {
		return nil, fmt.Errorf("failed to generate csrf secret: %w", err)
	}

	m := &Manager{
		shares:        make(map[string]*ShareEntry),
		cfg:           cfg,
		stateFilePath: statePath,
		csrfSecret:    secret,
	}

	m.loadState()
	return m, nil
}

func (m *Manager) CSRFSecret() []byte {
	return m.csrfSecret
}

func (m *Manager) GlobalActiveConns() int32 {
	return m.globalActiveConns.Load()
}

// loadState reads state.json and repins valid files.
func (m *Manager) loadState() {
	data, err := os.ReadFile(m.stateFilePath)
	if err != nil {
		return
	}

	var state PersistentState
	if err := json.Unmarshal(data, &state); err != nil {
		return
	}

	now := time.Now()
	for token, entry := range state.Shares {
		if now.After(entry.ExpiresAt) || entry.RemainingDownloads <= 0 {
			continue
		}

		f, fi, dev, ino, err := OpenAndPinFile(entry.FilePath)
		if err != nil {
			continue
		}

		// Verify metadata
		statT := fi.ModTime().UnixNano()
		if dev != entry.DeviceID || ino != entry.Inode || fi.Size() != entry.FileSize || statT != entry.MtimeUnixNano {
			_ = f.Close()
			continue
		}

		entry.InitRuntime(f)

		// Filter out expired sessions
		validSessions := make(map[string]*DownloadSession)
		for sid, sess := range entry.Sessions {
			if now.Before(sess.ExpiresAt) {
				validSessions[sid] = sess
			}
		}
		entry.Sessions = validSessions

		m.shares[token] = entry
	}
}

func (m *Manager) snapshotState() *PersistentState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	state := &PersistentState{
		Version: 3,
		Shares:  make(map[string]*ShareEntry, len(m.shares)),
	}

	for k, entry := range m.shares {
		entry.Mu.Lock()
		sessionsCopy := make(map[string]*DownloadSession, len(entry.Sessions))
		for sk, sv := range entry.Sessions {
			sessionsCopy[sk] = &DownloadSession{
				SessionID: sv.SessionID,
				SubnetKey: sv.SubnetKey,
				CreatedAt: sv.CreatedAt,
				ExpiresAt: sv.ExpiresAt,
			}
		}
		entryCopy := &ShareEntry{
			Token:              entry.Token,
			FilePath:           entry.FilePath,
			FileName:           entry.FileName,
			DeviceID:           entry.DeviceID,
			Inode:              entry.Inode,
			FileSize:           entry.FileSize,
			MtimeUnixNano:      entry.MtimeUnixNano,
			PasswordHash:       entry.PasswordHash,
			CreatedAt:          entry.CreatedAt,
			ExpiresAt:          entry.ExpiresAt,
			MaxDownloads:       entry.MaxDownloads,
			RemainingDownloads: entry.RemainingDownloads,
			Sessions:           sessionsCopy,
		}
		entry.Mu.Unlock()

		state.Shares[k] = entryCopy
	}

	return state
}

// SaveState atomically writes PersistentState snapshot to disk.
func (m *Manager) SaveState() error {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()

	state := m.snapshotState()

	dir := filepath.Dir(m.stateFilePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create state directory: %w", err)
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	tmpPath := fmt.Sprintf("%s.tmp.%d.%d", m.stateFilePath, os.Getpid(), time.Now().UnixNano())
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write state file: %w", err)
	}

	if err := os.Rename(tmpPath, m.stateFilePath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to commit state file: %w", err)
	}

	return nil
}

// CreateShare registers and pins a new file share.
func (m *Manager) CreateShare(filePath string, expiresDuration time.Duration, maxDownloads int, passwordHash string) (*ShareEntry, error) {
	if maxDownloads < 1 || maxDownloads > 1000 {
		return nil, fmt.Errorf("%w: max_downloads must be between 1 and 1000", ErrInvalidShareParams)
	}
	if expiresDuration < 1*time.Minute || expiresDuration > 720*time.Hour {
		return nil, fmt.Errorf("%w: expiration must be between 1m and 720h", ErrInvalidShareParams)
	}

	file, fi, dev, ino, err := OpenAndPinFile(filePath)
	if err != nil {
		return nil, err
	}

	token, err := crypto.GenerateToken()
	if err != nil {
		_ = file.Close()
		return nil, err
	}

	now := time.Now()
	entry := &ShareEntry{
		Token:              token,
		FilePath:           file.Name(),
		FileName:           filepath.Base(filePath),
		DeviceID:           dev,
		Inode:              ino,
		FileSize:           fi.Size(),
		MtimeUnixNano:      fi.ModTime().UnixNano(),
		PasswordHash:       passwordHash,
		CreatedAt:          now,
		ExpiresAt:          now.Add(expiresDuration),
		MaxDownloads:       maxDownloads,
		RemainingDownloads: maxDownloads,
		Sessions:           make(map[string]*DownloadSession),
	}
	entry.InitRuntime(file)

	m.mu.Lock()
	if len(m.shares) >= m.cfg.MaxActiveShares {
		m.mu.Unlock()
		entry.Close()
		return nil, fmt.Errorf("max active shares limit (%d) reached", m.cfg.MaxActiveShares)
	}
	m.shares[token] = entry
	m.mu.Unlock()

	go func() {
		_ = m.SaveState()
	}()

	return entry, nil
}

// GetShare retrieves an active ShareEntry by token.
func (m *Manager) GetShare(token string) (*ShareEntry, error) {
	m.mu.RLock()
	entry, exists := m.shares[token]
	m.mu.RUnlock()

	if !exists {
		return nil, ErrShareNotFound
	}

	if time.Now().After(entry.ExpiresAt) {
		m.RemoveShare(token)
		return nil, ErrShareExpired
	}

	return entry, nil
}

// RemoveShare removes a share, closes file handle, and saves state.
func (m *Manager) RemoveShare(token string) {
	m.mu.Lock()
	entry, exists := m.shares[token]
	if exists {
		delete(m.shares, token)
	}
	m.mu.Unlock()

	if exists {
		entry.Close()
		go func() {
			_ = m.SaveState()
		}()
	}
}

// RevokeShare revokes shares matching full token, token prefix (>= 6 chars), or real file path.
func (m *Manager) RevokeShare(target string) ([]string, error) {
	m.mu.Lock()
	var revoked []string
	var toClose []*ShareEntry

	for token, entry := range m.shares {
		match := false
		if token == target {
			match = true
		} else if len(target) >= 6 && strings.HasPrefix(token, target) {
			match = true
		} else if entry.FilePath == target || entry.FileName == target {
			match = true
		}

		if match {
			revoked = append(revoked, token)
			toClose = append(toClose, entry)
			delete(m.shares, token)
		}
	}
	m.mu.Unlock()

	for _, entry := range toClose {
		entry.Close()
	}

	if len(revoked) > 0 {
		_ = m.SaveState()
	}

	return revoked, nil
}

// ListShares returns all current active shares.
func (m *Manager) ListShares() []*ShareEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	now := time.Now()
	list := make([]*ShareEntry, 0, len(m.shares))
	for _, entry := range m.shares {
		if now.Before(entry.ExpiresAt) {
			list = append(list, entry)
		}
	}
	return list
}

// ClaimSession decrements 1 remaining download quota atomically and generates a session.
func (m *Manager) ClaimSession(token string, clientIP net.IP) (*ShareEntry, *DownloadSession, error) {
	entry, err := m.GetShare(token)
	if err != nil {
		return nil, nil, err
	}

	entry.Mu.Lock()
	defer entry.Mu.Unlock()

	if time.Now().After(entry.ExpiresAt) {
		return nil, nil, ErrShareExpired
	}

	if entry.RemainingDownloads <= 0 {
		return nil, nil, ErrDownloadExhausted
	}

	sessionID, err := crypto.GenerateToken()
	if err != nil {
		return nil, nil, err
	}

	now := time.Now()
	sessionExpires := now.Add(2 * time.Hour)
	if entry.ExpiresAt.Before(sessionExpires) {
		sessionExpires = entry.ExpiresAt
	}

	subnetKey := netutil.SessionSubnetKey(clientIP)
	session := &DownloadSession{
		SessionID: sessionID,
		SubnetKey: subnetKey,
		CreatedAt: now,
		ExpiresAt: sessionExpires,
	}

	// LRU eviction if sessions >= 128
	if len(entry.Sessions) >= 128 {
		var oldestKey string
		var oldestTime time.Time
		for k, s := range entry.Sessions {
			if s.ActiveConns.Load() == 0 {
				if oldestKey == "" || s.CreatedAt.Before(oldestTime) {
					oldestKey = k
					oldestTime = s.CreatedAt
				}
			}
		}
		if oldestKey != "" {
			delete(entry.Sessions, oldestKey)
		}
	}

	entry.RemainingDownloads--
	entry.Sessions[sessionID] = session

	go func() {
		_ = m.SaveState()
	}()

	return entry, session, nil
}

// GetSession validates token, sessionID and subnet binding.
func (m *Manager) GetSession(token, sessionID string, clientIP net.IP) (*ShareEntry, *DownloadSession, error) {
	entry, err := m.GetShare(token)
	if err != nil {
		return nil, nil, err
	}

	entry.Mu.Lock()
	session, exists := entry.Sessions[sessionID]
	entry.Mu.Unlock()

	if !exists {
		return nil, nil, ErrShareNotFound
	}

	if time.Now().After(session.ExpiresAt) {
		return nil, nil, ErrShareExpired
	}

	expectedSubnet := netutil.SessionSubnetKey(clientIP)
	if session.SubnetKey != expectedSubnet {
		return nil, nil, ErrSubnetMismatch
	}

	return entry, session, nil
}

// AcquireStreamSlots attempts to acquire concurrency slots at all 3 tiers.
// Tiers: Global, Entry, Session.
// Returns a release function if successful, or ErrConcurrencyLimit.
func (m *Manager) AcquireStreamSlots(entry *ShareEntry, session *DownloadSession) (func(), error) {
	// 1. Check Global
	for {
		cur := m.globalActiveConns.Load()
		if int(cur) >= m.cfg.MaxGlobalStreams {
			return nil, ErrConcurrencyLimit
		}
		if m.globalActiveConns.CompareAndSwap(cur, cur+1) {
			break
		}
	}

	// 2. Check Entry
	for {
		cur := entry.ActiveConns.Load()
		if int(cur) >= m.cfg.MaxStreamsPerToken {
			m.globalActiveConns.Add(-1)
			return nil, ErrConcurrencyLimit
		}
		if entry.ActiveConns.CompareAndSwap(cur, cur+1) {
			break
		}
	}

	// 3. Check Session
	for {
		cur := session.ActiveConns.Load()
		if int(cur) >= m.cfg.MaxStreamsPerSession {
			entry.ActiveConns.Add(-1)
			m.globalActiveConns.Add(-1)
			return nil, ErrConcurrencyLimit
		}
		if session.ActiveConns.CompareAndSwap(cur, cur+1) {
			break
		}
	}

	var released atomic.Bool
	release := func() {
		if released.CompareAndSwap(false, true) {
			session.ActiveConns.Add(-1)
			entry.ActiveConns.Add(-1)
			m.globalActiveConns.Add(-1)
		}
	}

	return release, nil
}

// StartCleanupLoop periodically purges expired shares and sessions.
func (m *Manager) StartCleanupLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.cleanExpired()
			}
		}
	}()
}

func (m *Manager) cleanExpired() {
	now := time.Now()
	var toDelete []string
	var toClose []*ShareEntry

	m.mu.Lock()
	for token, entry := range m.shares {
		entry.Mu.Lock()
		rem := entry.RemainingDownloads
		numSessions := len(entry.Sessions)
		entry.Mu.Unlock()

		if now.After(entry.ExpiresAt) || (rem <= 0 && numSessions == 0) {
			toDelete = append(toDelete, token)
			toClose = append(toClose, entry)
			delete(m.shares, token)
			continue
		}

		// Clean expired sessions inside share
		entry.Mu.Lock()
		for sid, sess := range entry.Sessions {
			if now.After(sess.ExpiresAt) && sess.ActiveConns.Load() == 0 {
				delete(entry.Sessions, sid)
			}
		}
		entry.Mu.Unlock()
	}
	m.mu.Unlock()

	for _, entry := range toClose {
		entry.Close()
	}

	if len(toDelete) > 0 {
		_ = m.SaveState()
	}
}
