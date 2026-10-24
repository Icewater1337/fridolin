package testutil

import (
	"sync"

	"github.com/Icewater1337/fridolin/internal/hostsecrets"
)

// Secrets is a host secret store in memory (hostsecrets.Store and
// Deleter), for tests that must not depend on the OS's file modes or
// keychain.
type Secrets struct {
	mu sync.Mutex
	m  map[string]string
}

// NewSecrets is an empty store.
func NewSecrets() *Secrets { return &Secrets{m: map[string]string{}} }

func (s *Secrets) Get(name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[name]
	if !ok {
		return "", hostsecrets.ErrNotFound
	}
	return v, nil
}

func (s *Secrets) Set(name, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[name] = value
	return nil
}

func (s *Secrets) Has(name string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.m[name]
	return ok, nil
}

func (s *Secrets) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, name)
	return nil
}
