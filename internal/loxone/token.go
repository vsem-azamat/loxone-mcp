package loxone

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// tokenFile is the cached JSON Web Token, so a restart authenticates without the password and
// without filling the Miniserver's token storage. It is a secret: the file is 0600.
type tokenFile struct {
	User       string `json:"user"`
	Token      string `json:"token,omitempty"`
	ValidUntil int64  `json:"validUntil,omitempty"` // seconds since 2009
	HashAlg    string `json:"hashAlg,omitempty"`
	ClientUUID string `json:"clientUUID"`
}

type tokenStore struct {
	path     string // "" keeps the token in memory only
	readOnly bool   // use the cached token but never write or refresh it
	mu       sync.Mutex
	mem      tokenFile
}

func (s *tokenStore) load(user string) tokenFile {
	empty := tokenFile{User: user}
	if s.path == "" {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.mem.User == user {
			return s.mem
		}
		return empty
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return empty
	}
	var t tokenFile
	if json.Unmarshal(b, &t) != nil {
		return empty
	}
	if t.User != user {
		// another user's token is useless, but the client identity can stay
		return tokenFile{User: user, ClientUUID: t.ClientUUID}
	}
	return t
}

func (s *tokenStore) save(t tokenFile) error {
	if s.readOnly {
		return nil
	}
	if s.path == "" {
		s.mu.Lock()
		s.mem = t
		s.mu.Unlock()
		return nil
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("token directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // a directory needs x; 0700 is owner only
		return fmt.Errorf("token directory: %w", err)
	}
	b, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("token file: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".token-*")
	if err != nil {
		return fmt.Errorf("token file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("token file: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("token file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("token file: %w", err)
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return fmt.Errorf("token file: %w", err)
	}
	return nil
}

func (s *tokenStore) clear(user string) {
	t := s.load(user)
	t.Token, t.ValidUntil, t.HashAlg = "", 0, ""
	_ = s.save(t)
}

// newClientUUID makes a UUID in the Miniserver's format (8-4-4-16 hex digits), which names
// this client in the user's token list.
func newClientUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:16])
}
