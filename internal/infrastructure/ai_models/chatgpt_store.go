// Package chatgpt implements the public-client Sign in with ChatGPT flow.
package ai_models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
	"golang.org/x/net/http/httpproxy"
)

const (
	BaseURL       = "https://api.openai.com/v1/"
	Issuer        = "https://auth.openai.com"
	DirectScope   = "tokens.use.direct"
	CredentialRef = "chatgpt-main"
)

type Credential struct {
	ClientID     string    `json:"client_id"`
	Subject      string    `json:"subject"`
	Email        string    `json:"email"`
	IDToken      string    `json:"id_token"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	Scopes       []string  `json:"scopes"`
}

type authFile struct {
	HostID      string                `json:"host_id"`
	Credentials map[string]Credential `json:"credentials"`
}

type Store struct {
	path     string
	http     *http.Client
	tokenURL string
}

func NewStore(homeDir string) *Store {
	base := http.DefaultTransport
	if standard, ok := base.(*http.Transport); ok {
		outbound := standard.Clone()
		// Read the environment injected by ApplyEnvFile, independently of the
		// process-wide ProxyFromEnvironment cache initialized by other packages.
		proxy := httpproxy.FromEnvironment().ProxyFunc()
		outbound.Proxy = func(req *http.Request) (*url.URL, error) { return proxy(req.URL) }
		base = outbound
	}
	return &Store{path: layout.OAuthCredentials(homeDir), http: &http.Client{Transport: base, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, tokenURL: Issuer + "/api/accounts/oauth/token"}
}

// withFile serializes read/refresh/write across clients and processes. Once a
// rotating refresh starts, persistence uses a bounded independent context so a
// cancelled chat cannot discard the only valid replacement refresh token.
func (s *Store) withFile(ctx context.Context, fn func(*authFile) (bool, error)) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	for {
		locked, lockErr := tryFileLock(lock)
		if lockErr != nil {
			return lockErr
		}
		if locked {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	defer unlockFile(lock)
	if err := ctx.Err(); err != nil {
		return err
	}
	doc := authFile{Credentials: map[string]Credential{}}
	data, err := os.ReadFile(s.path)
	if err == nil {
		if err := json.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("read ChatGPT credentials: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if doc.Credentials == nil {
		doc.Credentials = map[string]Credential{}
	}
	changed, err := fn(&doc)
	if err != nil || !changed {
		return err
	}
	data, err = json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(s.path), ".auth-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), s.path)
}

func (s *Store) registration(ctx context.Context) (string, Credential, error) {
	var host string
	var credential Credential
	err := s.withFile(ctx, func(doc *authFile) (bool, error) {
		changed := doc.HostID == ""
		if changed {
			doc.HostID = "urn:uuid:" + uuid.NewString()
		}
		host, credential = doc.HostID, doc.Credentials[CredentialRef]
		return changed, nil
	})
	return host, credential, err
}

func (s *Store) Save(ctx context.Context, ref string, credential Credential) error {
	return s.withFile(ctx, func(doc *authFile) (bool, error) { doc.Credentials[ref] = credential; return true, nil })
}

func (s *Store) AccessToken(ctx context.Context, ref string) (string, error) {
	var token string
	err := s.withFile(ctx, func(doc *authFile) (bool, error) {
		credential, ok := doc.Credentials[ref]
		if !ok || credential.AccessToken == "" {
			return false, errors.New("ChatGPT is not connected; sign in again")
		}
		if !slices.Contains(credential.Scopes, DirectScope) {
			return false, errors.New("ChatGPT plan usage was not authorized")
		}
		if time.Until(credential.ExpiresAt) > 5*time.Minute {
			token = credential.AccessToken
			return false, nil
		}
		refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		updated, err := s.requestToken(refreshCtx, url.Values{"grant_type": {"refresh_token"}, "client_id": {credential.ClientID}, "refresh_token": {credential.RefreshToken}, "resource": {strings.TrimRight(BaseURL, "/")}})
		if err != nil {
			return false, fmt.Errorf("refresh ChatGPT credentials: %w", err)
		}
		updated.ClientID, updated.Subject, updated.Email = credential.ClientID, credential.Subject, credential.Email
		// Keep the ID token whose identity was verified during sign-in. It can
		// remain a returning-login hint after expiry.
		updated.IDToken = credential.IDToken
		doc.Credentials[ref] = updated
		token = updated.AccessToken
		return true, nil
	})
	return token, err
}

func (s *Store) requestToken(ctx context.Context, form url.Values) (Credential, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Credential{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.http.Do(req)
	if err != nil {
		return Credential{}, errors.New("ChatGPT token endpoint unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Never include the token response or request URL in diagnostics.
		return Credential{}, fmt.Errorf("ChatGPT token exchange failed (HTTP %d); reconnect ChatGPT", resp.StatusCode)
	}
	var result struct {
		Access    string `json:"access_token"`
		Refresh   string `json:"refresh_token"`
		IDToken   string `json:"id_token"`
		ExpiresIn int64  `json:"expires_in"`
		Scope     string `json:"scope"`
		TokenType string `json:"token_type"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return Credential{}, errors.New("invalid ChatGPT token response")
	}
	scopes := strings.Fields(result.Scope)
	if result.Access == "" || result.Refresh == "" || result.ExpiresIn <= 0 || !strings.EqualFold(result.TokenType, "Bearer") || !slices.Contains(scopes, DirectScope) {
		return Credential{}, errors.New("ChatGPT did not grant valid credentials and plan usage permission")
	}
	return Credential{AccessToken: result.Access, RefreshToken: result.Refresh, IDToken: result.IDToken, Scopes: scopes, ExpiresAt: time.Now().Add(time.Duration(result.ExpiresIn) * time.Second)}, nil
}
