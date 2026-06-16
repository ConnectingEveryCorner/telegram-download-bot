package tgbot

import (
	"os"
	"path/filepath"
	"sort"
	"sync"

	"gopkg.in/yaml.v3"
)

type configManager struct {
	path string
	mu   sync.Mutex
}

type fileConfig struct {
	Bot struct {
		Token    string `yaml:"token"`
		Debug    bool   `yaml:"debug"`
		Language string `yaml:"language"`
	} `yaml:"bot"`
	Telegram struct {
		AppID   int    `yaml:"app-id"`
		AppHash string `yaml:"app-hash"`
	} `yaml:"telegram"`
	Auth struct {
		AdminChatID    int64   `yaml:"admin-chat-id"`
		AllowedChatIDs []int64 `yaml:"allowed-chat-ids"`
	} `yaml:"auth"`
	TDL map[string]any `yaml:"tdl"`
}

type authSnapshot struct {
	AdminChatID    int64
	AllowedChatIDs []int64
}

func newConfigManager(path string) *configManager {
	return &configManager{path: path}
}

func (m *configManager) auth() (authSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cfg, err := m.readLocked()
	if err != nil {
		return authSnapshot{}, err
	}
	cfg.Auth.AllowedChatIDs = normalizeIDs(cfg.Auth.AllowedChatIDs)
	return authSnapshot{
		AdminChatID:    cfg.Auth.AdminChatID,
		AllowedChatIDs: append([]int64(nil), cfg.Auth.AllowedChatIDs...),
	}, nil
}

func (m *configManager) grant(chatID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cfg, err := m.readLocked()
	if err != nil {
		return err
	}

	cfg.Auth.AllowedChatIDs = normalizeIDs(append(cfg.Auth.AllowedChatIDs, chatID))
	return m.writeLocked(cfg)
}

func (m *configManager) revoke(chatID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cfg, err := m.readLocked()
	if err != nil {
		return err
	}

	filtered := make([]int64, 0, len(cfg.Auth.AllowedChatIDs))
	for _, id := range cfg.Auth.AllowedChatIDs {
		if id != chatID {
			filtered = append(filtered, id)
		}
	}
	cfg.Auth.AllowedChatIDs = normalizeIDs(filtered)
	return m.writeLocked(cfg)
}

func (m *configManager) readLocked() (fileConfig, error) {
	var cfg fileConfig

	data, err := os.ReadFile(m.path)
	if err != nil {
		return cfg, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (m *configManager) writeLocked(cfg fileConfig) error {
	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(m.path, data, 0o600)
}

func normalizeIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
