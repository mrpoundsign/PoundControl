// Package secrets provides cross-platform OS keyring credential storage with an in-memory fallback.
package secrets

import (
	"errors"
	"fmt"
	"sync"

	"github.com/zalando/go-keyring"
)

// ServiceName is the default OS credential manager service identifier for PoundControl.
const ServiceName = "PoundControl"

// ErrNotFound is returned when a requested secret does not exist in any backing store.
var ErrNotFound = errors.New("secret not found")

// Store defines secure secret storage operations.
type Store interface {
	Set(key, secret string) error
	Get(key string) (string, error)
	Delete(key string) error
}

// MemoryStore is an in-memory Store suitable for tests and ephemeral sessions.
type MemoryStore struct {
	mu      sync.RWMutex
	secrets map[string]string
}

// NewMemoryStore creates an empty thread-safe in-memory secret store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		secrets: make(map[string]string),
	}
}

// Set stores secret under key in memory.
func (m *MemoryStore) Set(key, secret string) error {
	m.mu.Lock()
	m.secrets[key] = secret
	m.mu.Unlock()
	return nil
}

// Get retrieves the secret stored under key, returning ErrNotFound if absent.
func (m *MemoryStore) Get(key string) (string, error) {
	m.mu.RLock()
	val, ok := m.secrets[key]
	m.mu.RUnlock()
	if !ok {
		return "", ErrNotFound
	}
	return val, nil
}

// Delete removes any secret stored under key.
func (m *MemoryStore) Delete(key string) error {
	m.mu.Lock()
	delete(m.secrets, key)
	m.mu.Unlock()
	return nil
}

type keyringProvider interface {
	Set(service, user, password string) error
	Get(service, user string) (string, error)
	Delete(service, user string) error
}

type osKeyringProvider struct{}

func (osKeyringProvider) Set(service, user, password string) error {
	return keyring.Set(service, user, password)
}

func (osKeyringProvider) Get(service, user string) (string, error) {
	return keyring.Get(service, user)
}

func (osKeyringProvider) Delete(service, user string) error {
	return keyring.Delete(service, user)
}

// KeyringStore persists secrets in the OS credential manager with an in-memory cache and fallback.
type KeyringStore struct {
	service  string
	provider keyringProvider
	fallback *MemoryStore
	mu       sync.RWMutex
	cache    map[string]string
	notFound map[string]bool
}

// NewKeyringStore constructs a KeyringStore bound to service (defaulting to ServiceName if empty).
func NewKeyringStore(service string) *KeyringStore {
	return newKeyringStoreWithProvider(service, osKeyringProvider{})
}

func newKeyringStoreWithProvider(service string, provider keyringProvider) *KeyringStore {
	if service == "" {
		service = ServiceName
	}
	if provider == nil {
		provider = osKeyringProvider{}
	}
	return &KeyringStore{
		service:  service,
		provider: provider,
		fallback: NewMemoryStore(),
		cache:    make(map[string]string),
		notFound: make(map[string]bool),
	}
}

// Set saves secret in the OS keyring, falling back to memory if the OS keyring returns an error.
func (k *KeyringStore) Set(key, secret string) error {
	k.mu.Lock()
	k.cache[key] = secret
	delete(k.notFound, key)
	k.mu.Unlock()

	if err := k.provider.Set(k.service, key, secret); err != nil {
		_ = k.fallback.Set(key, secret)
	}
	return nil
}

// Get retrieves secret from cache, the OS keyring, or the in-memory fallback store.
func (k *KeyringStore) Get(key string) (string, error) {
	k.mu.RLock()
	if val, ok := k.cache[key]; ok {
		k.mu.RUnlock()
		return val, nil
	}
	if k.notFound[key] {
		k.mu.RUnlock()
		return "", ErrNotFound
	}
	k.mu.RUnlock()

	val, err := k.provider.Get(k.service, key)
	if err == nil {
		k.mu.Lock()
		k.cache[key] = val
		delete(k.notFound, key)
		k.mu.Unlock()
		return val, nil
	}

	if fbVal, fbErr := k.fallback.Get(key); fbErr == nil {
		k.mu.Lock()
		k.cache[key] = fbVal
		delete(k.notFound, key)
		k.mu.Unlock()
		return fbVal, nil
	}

	if errors.Is(err, keyring.ErrNotFound) {
		k.mu.Lock()
		k.notFound[key] = true
		k.mu.Unlock()
		return "", ErrNotFound
	}
	return "", fmt.Errorf("retrieve secret %q: %w", key, err)
}

// Delete removes secret from cache, fallback storage, and the OS keyring.
func (k *KeyringStore) Delete(key string) error {
	k.mu.Lock()
	delete(k.cache, key)
	k.notFound[key] = true
	k.mu.Unlock()

	_ = k.fallback.Delete(key)
	_ = k.provider.Delete(k.service, key)
	return nil
}
