package ai_models

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type LoginStatus struct {
	ID        string    `json:"login_id"`
	URL       string    `json:"authorization_url,omitempty"`
	State     string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
	Models    []Model   `json:"models,omitempty"`
}

type LoginManager struct {
	mu           sync.Mutex
	store        *Store
	status       LoginStatus
	cancel       context.CancelFunc
	discoveryURL string
	listenAddr   string
}

func NewLoginManager(homeDir string) *LoginManager {
	return &LoginManager{store: NewStore(homeDir), discoveryURL: Issuer + "/.well-known/openid-configuration", listenAddr: "127.0.0.1:0"}
}

func randomValue() string {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(data)
}

func (m *LoginManager) Start(ctx context.Context) (LoginStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status.State == "pending" {
		return LoginStatus{}, errors.New("a ChatGPT sign-in is already pending")
	}
	host, previous, err := m.store.registration(ctx)
	if err != nil {
		return LoginStatus{}, err
	}
	var discovery struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	if err := m.store.getJSON(ctx, m.discoveryURL, &discovery); err != nil {
		return LoginStatus{}, err
	}
	keysURL, err := url.Parse(discovery.JWKSURI)
	if err != nil || discovery.Issuer != Issuer || keysURL.Scheme != "https" || keysURL.Host != "auth.openai.com" {
		return LoginStatus{}, errors.New("invalid OpenAI identity configuration")
	}
	listener, err := net.Listen("tcp", m.listenAddr)
	if err != nil {
		return LoginStatus{}, errors.New("ChatGPT callback port is unavailable; finish or cancel other pending sign-ins")
	}
	redirect := "http://" + listener.Addr().String() + "/auth/callback"
	state, nonce, verifier := randomValue(), randomValue(), randomValue()
	challenge := sha256.Sum256([]byte(verifier))
	form := url.Values{"response_type": {"code"}, "redirect_uri": {redirect}, "resource": {strings.TrimRight(BaseURL, "/")}, "scope": {"openid profile email offline_access resource.invoke " + DirectScope}, "state": {state}, "nonce": {nonce}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "ext_agent_host_id": {host}}
	if previous.ClientID == "" {
		form.Set("client_id", "dynamic_agent_client")
		form.Set("agent_name_hint", "LaxCode")
	} else {
		form.Set("client_id", previous.ClientID)
	}
	id := randomValue()
	loginCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
	m.cancel = cancel
	m.status = LoginStatus{ID: id, URL: Issuer + "/api/accounts/authorize?" + form.Encode(), State: "pending", ExpiresAt: time.Now().Add(10 * time.Minute)}
	var callbackMu sync.Mutex
	used := false
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Method != http.MethodGet || r.URL.Path != "/auth/callback" {
			http.NotFound(w, r)
			return
		}
		query := r.URL.Query()
		if query.Get("state") != state {
			http.Error(w, "Invalid OAuth state", http.StatusBadRequest)
			return
		}
		callbackMu.Lock()
		if used {
			callbackMu.Unlock()
			http.Error(w, "Sign-in already handled", http.StatusConflict)
			return
		}
		used = true
		callbackMu.Unlock()
		clientID := query.Get("client_id")
		if previous.ClientID != "" {
			if clientID == "" {
				clientID = previous.ClientID
			}
			if clientID != previous.ClientID {
				m.finish(id, nil, errors.New("ChatGPT registration changed; sign-in rejected"))
				http.Error(w, "Registration mismatch", 400)
				cancel()
				return
			}
		}
		var models []Model
		err := func() error {
			if query.Get("error") != "" {
				return errors.New("ChatGPT authorization was declined")
			}
			if clientID == "" || clientID == "dynamic_agent_client" || query.Get("code") == "" {
				return errors.New("incomplete ChatGPT registration callback")
			}
			if previous.ClientID == "" {
				// Retain the issued registration even if its code expires.
				if err := m.store.Save(loginCtx, CredentialRef, Credential{ClientID: clientID}); err != nil {
					return err
				}
			}
			credential, err := m.store.requestToken(loginCtx, url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {query.Get("code")}, "code_verifier": {verifier}, "redirect_uri": {redirect}, "resource": {strings.TrimRight(BaseURL, "/")}})
			if err != nil {
				return err
			}
			subject, email, err := m.store.verifyIdentity(loginCtx, credential.IDToken, clientID, nonce, discovery.JWKSURI)
			if err != nil {
				return err
			}
			if previous.Subject != "" && subject != previous.Subject {
				return errors.New("ChatGPT account differs from the saved registration")
			}
			credential.ClientID, credential.Subject, credential.Email = clientID, subject, email
			if err := m.store.Save(loginCtx, CredentialRef, credential); err != nil {
				return err
			}
			models, err = m.store.Models(loginCtx, CredentialRef)
			return err
		}()
		m.finish(id, models, err)
		if err != nil {
			http.Error(w, "ChatGPT sign-in failed. Return to LaxCode for details.", 400)
		} else {
			fmt.Fprint(w, "ChatGPT connected. You can close this window and return to LaxCode.")
		}
		cancel()
	})
	go func() { _ = server.Serve(listener) }()
	go func() {
		<-loginCtx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
		m.mu.Lock()
		if m.status.ID == id && m.status.State == "pending" {
			m.status.State = "failed"
			m.status.Error = "ChatGPT sign-in expired or was cancelled"
		}
		m.mu.Unlock()
	}()
	return m.status, nil
}

func (m *LoginManager) finish(id string, models []Model, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status.ID != id || m.status.State != "pending" {
		return
	}
	m.status.URL = ""
	if err != nil {
		m.status.State = "failed"
		m.status.Error = err.Error()
	} else {
		m.status.State = "connected"
		m.status.Models = models
	}
}

func (m *LoginManager) Status(id string) (LoginStatus, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := m.status
	result.URL = "" // Keep per-attempt authorization data out of polling responses.
	return result, result.ID == id && id != ""
}

func (m *LoginManager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
	}
	if m.status.State == "pending" {
		m.status.State = "failed"
		m.status.URL = ""
		m.status.Error = "ChatGPT sign-in cancelled"
	}
}

func (s *Store) getJSON(ctx context.Context, address string, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return errors.New("invalid ChatGPT identity endpoint")
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return errors.New("ChatGPT identity endpoint unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ChatGPT identity endpoint failed (HTTP %d)", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(result); err != nil {
		return errors.New("invalid ChatGPT identity response")
	}
	return nil
}

// Verify the signature before trusting claims. The algorithm is pinned to RS256;
// keys and URLs embedded in the untrusted token are never followed.
func (s *Store) verifyIdentity(ctx context.Context, token, clientID, nonce, jwksURL string) (string, string, error) {
	invalid := errors.New("ChatGPT ID token verification failed")
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", "", invalid
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", "", invalid
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if json.Unmarshal(headerBytes, &header) != nil || header.Alg != "RS256" || header.Kid == "" {
		return "", "", invalid
	}
	var jwks struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			Alg string `json:"alg"`
			Use string `json:"use"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := s.getJSON(ctx, jwksURL, &jwks); err != nil {
		return "", "", err
	}
	verified := false
	for _, key := range jwks.Keys {
		if key.Kid != header.Kid || key.Kty != "RSA" || (key.Alg != "" && key.Alg != "RS256") || (key.Use != "" && key.Use != "sig") {
			continue
		}
		n, e1 := base64.RawURLEncoding.DecodeString(key.N)
		exponent, e2 := base64.RawURLEncoding.DecodeString(key.E)
		if e1 != nil || e2 != nil || len(exponent) > 4 {
			continue
		}
		e := 0
		for _, b := range exponent {
			e = e*256 + int(b)
		}
		if e < 3 || e%2 == 0 || len(n) < 256 {
			continue
		}
		signature, err := base64.RawURLEncoding.DecodeString(parts[2])
		if err != nil {
			continue
		}
		digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if rsa.VerifyPKCS1v15(&rsa.PublicKey{N: new(big.Int).SetBytes(n), E: e}, crypto.SHA256, digest[:], signature) == nil {
			verified = true
			break
		}
	}
	if !verified {
		return "", "", invalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", invalid
	}
	var claims struct {
		Issuer          string          `json:"iss"`
		Subject         string          `json:"sub"`
		Email           string          `json:"email"`
		Audience        json.RawMessage `json:"aud"`
		AuthorizedParty string          `json:"azp"`
		Nonce           string          `json:"nonce"`
		Expires         int64           `json:"exp"`
		Issued          int64           `json:"iat"`
		NotBefore       int64           `json:"nbf"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return "", "", invalid
	}
	var audience []string
	var single string
	if json.Unmarshal(claims.Audience, &single) == nil {
		audience = []string{single}
	} else if json.Unmarshal(claims.Audience, &audience) != nil {
		return "", "", invalid
	}
	found := false
	for _, value := range audience {
		if value == clientID {
			found = true
		}
	}
	now := time.Now().Unix()
	if !found || (len(audience) > 1 && claims.AuthorizedParty != clientID) || (claims.AuthorizedParty != "" && claims.AuthorizedParty != clientID) || claims.Issuer != Issuer || claims.Subject == "" || claims.Nonce != nonce || claims.Expires <= now || claims.Issued > now+30 || claims.NotBefore > now+30 {
		return "", "", invalid
	}
	return claims.Subject, claims.Email, nil
}
