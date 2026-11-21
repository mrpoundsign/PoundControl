package secrets

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

type fakeKeyringProvider struct {
	data   map[string]string
	setErr error
	getErr error
}

func newFakeKeyringProvider() *fakeKeyringProvider {
	return &fakeKeyringProvider{data: make(map[string]string)}
}

func (f *fakeKeyringProvider) Set(service, user, password string) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.data[service+":"+user] = password
	return nil
}

func (f *fakeKeyringProvider) Get(service, user string) (string, error) {
	if f.getErr != nil {
		return "", f.getErr
	}
	v, ok := f.data[service+":"+user]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return v, nil
}

func (f *fakeKeyringProvider) Delete(service, user string) error {
	delete(f.data, service+":"+user)
	return nil
}

func TestMemoryStoreCRUD(t *testing.T) {
	store := NewMemoryStore()
	if _, err := store.Get("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := store.Set("k1", "s3cr3t"); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	got, err := store.Get("k1")
	if err != nil || got != "s3cr3t" {
		t.Fatalf("Get() = (%q, %v), want (s3cr3t, nil)", got, err)
	}
	if err := store.Delete("k1"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := store.Get("k1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after Delete, got %v", err)
	}
}

func TestKeyringStoreWithFakeProviderAndFallback(t *testing.T) {
	fake := newFakeKeyringProvider()
	store := newKeyringStoreWithProvider("", fake)

	if _, err := store.Get("obs.pw"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound initially, got %v", err)
	}

	if err := store.Set("obs.pw", "hunter2"); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if got := fake.data[ServiceName+":obs.pw"]; got != "hunter2" {
		t.Fatalf("expected fake provider stored secret, got %q", got)
	}

	// Read from a fresh KeyringStore sharing the same provider to bypass cache.
	store2 := newKeyringStoreWithProvider(ServiceName, fake)
	got, err := store2.Get("obs.pw")
	if err != nil || got != "hunter2" {
		t.Fatalf("store2.Get() = (%q, %v), want (hunter2, nil)", got, err)
	}

	if err := store.Delete("obs.pw"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := store.Get("obs.pw"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound on store after Delete, got %v", err)
	}
	store3 := newKeyringStoreWithProvider(ServiceName, fake)
	if _, err := store3.Get("obs.pw"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound in backing provider after Delete, got %v", err)
	}

	// Provider errors fall back to in-memory storage cleanly.
	failing := newFakeKeyringProvider()
	failing.setErr = errors.New("keyring daemon unavailable")
	failing.getErr = errors.New("keyring daemon unavailable")
	fbStore := newKeyringStoreWithProvider(ServiceName, failing)
	if err := fbStore.Set("obs.pw", "fallback-pw"); err != nil {
		t.Fatalf("Set() fallback error = %v", err)
	}
	// Clear cache to test fallback store directly.
	fbStore.mu.Lock()
	clear(fbStore.cache)
	fbStore.mu.Unlock()

	got, err = fbStore.Get("obs.pw")
	if err != nil || got != "fallback-pw" {
		t.Fatalf("Get() fallback = (%q, %v), want (fallback-pw, nil)", got, err)
	}
}
