package deck

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Store persists and loads PoundControl configuration trees.
type Store interface {
	Load() (*Config, error)
	Save(cfg *Config) error
}

// MemoryStore is an in-memory Config store suitable for isolated unit and UI tests.
type MemoryStore struct {
	mu  sync.Mutex
	cfg *Config
}

// NewMemoryStore creates a thread-safe in-memory store seeded with a clone of initial (or DefaultConfig if nil).
func NewMemoryStore(initial *Config) *MemoryStore {
	if initial == nil {
		initial = DefaultConfig()
	} else {
		initial = initial.Clone()
		initial.Normalize()
	}
	return &MemoryStore{cfg: initial}
}

// Load returns a deep copy of the stored configuration.
func (m *MemoryStore) Load() (*Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg == nil {
		m.cfg = DefaultConfig()
	}
	return m.cfg.Clone(), nil
}

// Save validates and stores a deep copy of cfg.
func (m *MemoryStore) Save(cfg *Config) error {
	if cfg == nil {
		return errors.New("cannot save nil config")
	}
	cp := cfg.Clone()
	cp.Normalize()
	if err := cp.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	m.cfg = cp
	m.mu.Unlock()
	return nil
}

// FileStore persists configuration JSON atomically to a specific file path on disk.
type FileStore struct {
	mu   sync.Mutex
	path string
}

// NewFileStore constructs a FileStore targeting path.
func NewFileStore(path string) *FileStore {
	return &FileStore{path: path}
}

// Path returns the target file path for this store.
func (f *FileStore) Path() string {
	return f.path
}

// Load reads and validates the configuration from disk, returning DefaultConfig if the file does not exist yet.
func (f *FileStore) Load() (*Config, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	data, err := os.ReadFile(f.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DefaultConfig(), nil
		}
		return nil, fmt.Errorf("read config %q: %w", f.path, err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", f.path, err)
	}
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config %q: %w", f.path, err)
	}
	return &cfg, nil
}

// Save validates and writes cfg atomically via a temporary file in the same directory.
func (f *FileStore) Save(cfg *Config) error {
	if cfg == nil {
		return errors.New("cannot save nil config")
	}
	cp := cfg.Clone()
	cp.Normalize()
	if err := cp.Validate(); err != nil {
		return err
	}

	raw, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	raw = append(raw, '\n')

	f.mu.Lock()
	defer f.mu.Unlock()

	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create config directory %q: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp config file: %w", err)
	}
	tmpPath := tmp.Name()
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp config file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp config file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config file: %w", err)
	}
	if err := os.Rename(tmpPath, f.path); err != nil {
		return fmt.Errorf("replace config file %q: %w", f.path, err)
	}
	renamed = true
	return nil
}

// DefaultConfigPath returns the standard per-user PoundControl configuration path.
// Tests must NOT call this helper; pass an explicit t.TempDir() path or MemoryStore instead.
func DefaultConfigPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config dir: %w", err)
	}
	return filepath.Join(base, "PoundControl", "config.json"), nil
}
