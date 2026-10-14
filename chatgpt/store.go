// Package chatgpt implements the public Sign in with ChatGPT flow. Tokens never
// enter LLM profiles or browser responses; profiles contain only an account ID.
package chatgpt

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

type Account struct {
	Generation        uint64    `json:"generation"`
	PendingIDToken    string    `json:"pending_id_token,omitempty"`
	ID                string    `json:"id"`
	ClientID          string    `json:"client_id"`
	Subject           string    `json:"subject"`
	Email             string    `json:"email"`
	AccessToken       string    `json:"access_token,omitempty"`
	RefreshToken      string    `json:"refresh_token,omitempty"`
	IDToken           string    `json:"id_token,omitempty"`
	Scope             string    `json:"scope"`
	ExpiresAt         time.Time `json:"expires_at"`
	EarliestRefreshAt int64     `json:"earliest_refresh_at,omitempty"`
}

type AccountView struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	ClientID    string    `json:"client_id"`
	Connected   bool      `json:"connected"`
	PlanEnabled bool      `json:"plan_enabled"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func (a Account) view() AccountView {
	return AccountView{a.ID, a.Email, a.ClientID, a.AccessToken != "", hasScope(a.Scope, "chatgpt.tokens.use.direct"), a.ExpiresAt}
}
func (a *Account) clear() {
	a.AccessToken = ""
	a.RefreshToken = ""
	a.IDToken = ""
	a.PendingIDToken = ""
	a.Scope = ""
	a.ExpiresAt = time.Time{}
	a.EarliestRefreshAt = 0
}

type diskState struct {
	HostID   string              `json:"host_id"`
	Accounts map[string]*Account `json:"accounts"`
}
type store struct {
	dir  string
	aead cipher.AEAD
}

func newStore(dir string, key []byte) (*store, error) {
	if len(key) < 32 {
		return nil, errors.New("ChatGPT credential encryption requires a 32-byte key")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	derived := sha256.Sum256(append([]byte("ARTEX ChatGPT credentials v1\x00"), key...))
	block, err := aes.NewCipher(derived[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	return &store{dir, aead}, err
}

// The OS lock serializes rotating tokens across server processes as well as
// goroutines. Always reload disk under the lock; never refresh a cached token.
func (s *store) with(ctx context.Context, fn func(*diskState) (bool, error)) error {
	f, err := os.OpenFile(filepath.Join(s.dir, "credentials.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		ok, e := tryLock(f)
		if e != nil {
			return e
		}
		if ok {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	defer unlock(f)
	d := diskState{HostID: "urn:uuid:" + uuid.NewString(), Accounts: map[string]*Account{}}
	b, err := os.ReadFile(filepath.Join(s.dir, "credentials.enc"))
	if err == nil {
		n := s.aead.NonceSize()
		if len(b) < n {
			return errors.New("invalid ChatGPT credential file")
		}
		plain, e := s.aead.Open(nil, b[:n], b[n:], []byte("artex-chatgpt-v1"))
		if e != nil {
			return errors.New("cannot decrypt ChatGPT credentials")
		}
		if e = json.Unmarshal(plain, &d); e != nil {
			return errors.New("invalid ChatGPT credentials")
		}
		if d.HostID == "" || d.Accounts == nil {
			return errors.New("invalid ChatGPT credential state")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	changed, opErr := fn(&d)
	if changed {
		if err = s.save(d); err != nil {
			return fmt.Errorf("persist ChatGPT credentials: %w", err)
		}
	}
	return opErr
}
func (s *store) save(d diskState) error {
	plain, err := json.Marshal(d)
	if err != nil {
		return err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	data := s.aead.Seal(nonce, nonce, plain, []byte("artex-chatgpt-v1"))
	f, err := os.CreateTemp(s.dir, ".credentials-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = io.Copy(f, bytes.NewReader(data)); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(s.dir, "credentials.enc"))
}
