package ai_models

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func jsonResponse(r *http.Request, value any) *http.Response {
	data, _ := json.Marshal(value)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(data))), Request: r}
}
func validCredential() Credential {
	return Credential{ClientID: "issued-client", Subject: "user", AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour), Scopes: []string{DirectScope}}
}

func TestOAuthRequestsUseEnvironmentProxy(t *testing.T) {
	// Use a fresh process to simulate a dependency caching an empty proxy
	// environment before ApplyEnvFile injects the user's configuration.
	if os.Getenv("LAXCODE_CHATGPT_PROXY_TEST") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestOAuthRequestsUseEnvironmentProxy$")
		cmd.Env = append(os.Environ(), "LAXCODE_CHATGPT_PROXY_TEST=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("proxy test: %v\n%s", err, output)
		}
		return
	}
	var authRequests, apiRequests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			t.Errorf("expected HTTPS CONNECT, got %s", r.Method)
		}
		switch r.Host {
		case "auth.openai.com:443":
			authRequests.Add(1)
		case "api.openai.com:443":
			apiRequests.Add(1)
		default:
			t.Errorf("unexpected proxy destination: %s", r.Host)
		}
		// Stop at the proxy; this test never contacts OpenAI.
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	for _, key := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy", "REQUEST_METHOD"} {
		t.Setenv(key, "")
	}
	identityRequest, _ := http.NewRequest(http.MethodGet, Issuer, nil)
	_, _ = http.ProxyFromEnvironment(identityRequest)
	t.Setenv("https_proxy", proxy.URL)
	home := t.TempDir()
	store := NewStore(home)
	ctx := context.Background()
	if err := store.Save(ctx, CredentialRef, validCredential()); err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{Issuer + "/.well-known/openid-configuration", Issuer + "/jwks"} {
		if err := store.getJSON(ctx, address, &map[string]any{}); err == nil {
			t.Fatal("identity request bypassed the rejecting proxy")
		}
	}
	for _, grant := range []string{"authorization_code", "refresh_token"} {
		if _, err := store.requestToken(ctx, url.Values{"grant_type": {grant}}); err == nil {
			t.Fatal("token request bypassed the rejecting proxy")
		}
	}
	if _, err := store.Models(ctx, CredentialRef); err == nil {
		t.Fatal("model catalog request bypassed the rejecting proxy")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, BaseURL+"responses", strings.NewReader(`{"input":[]}`))
	if resp, err := HTTPClient(home, CredentialRef).Do(req); err == nil {
		resp.Body.Close()
		t.Fatal("Responses request bypassed the rejecting proxy")
	}
	if authRequests.Load() != 4 || apiRequests.Load() != 2 {
		t.Fatalf("proxy requests: auth=%d API=%d", authRequests.Load(), apiRequests.Load())
	}
}

func TestRefreshIsSerializedAndPersistsRotatingToken(t *testing.T) {
	home := t.TempDir()
	store := NewStore(home)
	credential := validCredential()
	credential.ExpiresAt = time.Now()
	if err := store.Save(context.Background(), CredentialRef, credential); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	fake := roundTrip(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("client_id") != "issued-client" || r.Form.Get("refresh_token") != "refresh" || r.Form.Get("resource") != "https://api.openai.com/v1" || r.Form.Get("scope") != "" {
			t.Errorf("incorrect refresh form")
		}
		return jsonResponse(r, map[string]any{"access_token": "new-access", "refresh_token": "rotated-refresh", "expires_in": 3600, "scope": DirectScope, "token_type": "Bearer"}), nil
	})
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			s := NewStore(home)
			s.http.Transport = fake
			token, err := s.AccessToken(context.Background(), CredentialRef)
			if err != nil || token != "new-access" {
				t.Errorf("token=%q err=%v", token, err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("refreshes=%d", calls.Load())
	}
	data, err := os.ReadFile(store.path)
	if err != nil || !strings.Contains(string(data), "rotated-refresh") {
		t.Fatalf("replacement was not persisted: %v", err)
	}
	info, err := os.Stat(store.path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("credential file permissions: %v", err)
	}
	_, saved, err := store.registration(context.Background())
	if err != nil || saved.Subject != "user" || saved.ClientID != "issued-client" {
		t.Fatal("refresh lost account identity")
	}
}

func TestCancelledCallerCannotDiscardRefresh(t *testing.T) {
	store := NewStore(t.TempDir())
	credential := validCredential()
	credential.ExpiresAt = time.Now()
	if err := store.Save(context.Background(), CredentialRef, credential); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store.http.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		cancel()
		if r.Context().Err() != nil {
			t.Fatal("refresh inherited chat cancellation")
		}
		return jsonResponse(r, map[string]any{"access_token": "new-access", "refresh_token": "new-refresh", "expires_in": 3600, "scope": DirectScope, "token_type": "Bearer"}), nil
	})
	if _, err := store.AccessToken(ctx, CredentialRef); err != nil {
		t.Fatal(err)
	}
	_, saved, err := store.registration(context.Background())
	if err != nil || saved.RefreshToken != "new-refresh" {
		t.Fatal("cancelled request discarded the rotating token")
	}
}

func TestRefreshFailurePreservesCredentialsAndRedactsResponse(t *testing.T) {
	store := NewStore(t.TempDir())
	credential := validCredential()
	credential.ExpiresAt = time.Now()
	if err := store.Save(context.Background(), CredentialRef, credential); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(store.path)
	store.http.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		result := jsonResponse(r, map[string]string{"error": "secret-refresh-token"})
		result.StatusCode = 400
		return result, nil
	})
	_, err := store.AccessToken(context.Background(), CredentialRef)
	if err == nil || strings.Contains(err.Error(), "secret-refresh-token") {
		t.Fatalf("unsafe refresh error: %v", err)
	}
	after, _ := os.ReadFile(store.path)
	if string(before) != string(after) {
		t.Fatal("failed refresh changed credentials")
	}
}

func TestNormalizeRequestPreservesToolReplayAndReasoning(t *testing.T) {
	data, err := NormalizeRequest([]byte(`{"model":"gpt-6.1-sol","reasoning":{"effort":"high"},"max_output_tokens":4000,"store":true,"previous_response_id":"r","input":[{"role":"system","content":"rules"},{"type":"reasoning","id":"r-old","content":[]},{"type":"reasoning","id":"r-new","encrypted_content":"opaque","content":[]},{"type":"function_call","call_id":"c","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"c","output":"ok"}],"tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	if body["store"] != false || body["stream"] != true || body["max_output_tokens"] != nil || body["previous_response_id"] != nil {
		t.Fatalf("unsupported body fields: %s", data)
	}
	input := body["input"].([]any)
	if len(input) != 4 || input[0].(map[string]any)["role"] != "developer" || input[1].(map[string]any)["encrypted_content"] != "opaque" || input[1].(map[string]any)["content"] != nil || input[2].(map[string]any)["namespace"] != "laxcode" || input[3].(map[string]any)["call_id"] != "c" {
		t.Fatalf("replay changed: %s", data)
	}
	namespace := body["tools"].([]any)[0].(map[string]any)
	if namespace["type"] != "namespace" || namespace["name"] != "laxcode" || namespace["tools"].([]any)[0].(map[string]any)["name"] != "read" {
		t.Fatalf("tool namespace mismatch: %s", data)
	}
	if body["reasoning"].(map[string]any)["effort"] != "high" {
		t.Fatal("effort lost")
	}
	if _, err := NormalizeRequest([]byte(`{"input":"text"}`)); err == nil {
		t.Fatal("invalid input accepted")
	}
}

func TestTransportRefusesSendingCredentialsElsewhere(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Save(context.Background(), CredentialRef, validCredential()); err != nil {
		t.Fatal(err)
	}
	calls := 0
	tr := &transport{store: store, ref: CredentialRef, base: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "Bearer access" {
			t.Error("OAuth access token not applied")
		}
		return jsonResponse(r, map[string]bool{}), nil
	})}
	for _, address := range []string{"https://evil.example/v1/responses", "http://api.openai.com/v1/responses", "https://api.openai.com/v1/responses/input_tokens", "https://api.openai.com:443/v1/responses"} {
		req, _ := http.NewRequest("POST", address, strings.NewReader(`{"input":[]}`))
		if _, err := tr.RoundTrip(req); err == nil {
			t.Fatalf("unsafe endpoint accepted: %s", address)
		}
	}
	req, _ := http.NewRequest("POST", BaseURL+"responses", strings.NewReader(`{"input":[]}`))
	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("upstream calls=%d", calls)
	}
}

func signedToken(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "test"})
	payload, _ := json.Marshal(claims)
	message := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(message))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return message + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func TestDynamicLoginVerifiesCallbackIdentityAndImportsCatalog(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewLoginManager(t.TempDir())
	manager.listenAddr = "127.0.0.1:0"
	defer manager.Close()
	var nonce, challenge string
	var exchanges atomic.Int32
	manager.store.http.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			return jsonResponse(r, map[string]string{"issuer": Issuer, "jwks_uri": Issuer + "/jwks"}), nil
		case "/jwks":
			return jsonResponse(r, map[string]any{"keys": []any{map[string]string{"kid": "test", "kty": "RSA", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}}), nil
		case "/api/accounts/oauth/token":
			exchanges.Add(1)
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			verifierHash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if r.Form.Get("client_id") != "issued-client" || base64.RawURLEncoding.EncodeToString(verifierHash[:]) != challenge {
				t.Error("issued client or PKCE verifier mismatch")
			}
			idToken := signedToken(t, key, map[string]any{"iss": Issuer, "aud": "issued-client", "nonce": nonce, "sub": "user", "email": "user@example.com", "exp": time.Now().Add(time.Hour).Unix()})
			return jsonResponse(r, map[string]any{"access_token": "access", "refresh_token": "refresh", "id_token": idToken, "scope": DirectScope, "expires_in": 3600, "token_type": "Bearer"}), nil
		case "/v1/models":
			if r.Header.Get("Authorization") != "Bearer access" {
				t.Error("model catalog is not authenticated")
			}
			return jsonResponse(r, map[string]any{"models": []any{Model{Slug: "gpt-6.1-sol", DisplayName: "GPT-6.1 Sol", Visibility: "list"}, Model{Slug: "hidden", Visibility: "hidden"}}}), nil
		default:
			return nil, fmt.Errorf("unexpected test endpoint")
		}
	})
	status, err := manager.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	authorization, _ := url.Parse(status.URL)
	form := authorization.Query()
	nonce, challenge = form.Get("nonce"), form.Get("code_challenge")
	if form.Get("client_id") != "dynamic_agent_client" || form.Get("agent_name_hint") != "LaxCode" || !strings.HasPrefix(form.Get("ext_agent_host_id"), "urn:uuid:") || form.Get("code_challenge_method") != "S256" {
		t.Fatal("invalid registration parameters")
	}
	if _, err := manager.Start(context.Background()); err == nil {
		t.Fatal("concurrent login accepted")
	}
	callback, _ := url.Parse(form.Get("redirect_uri"))
	callback.RawQuery = url.Values{"code": {"test"}, "state": {"wrong"}, "client_id": {"issued-client"}}.Encode()
	response, err := http.Get(callback.String())
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 400 || exchanges.Load() != 0 {
		t.Fatal("state mismatch reached token exchange")
	}
	callback.RawQuery = url.Values{"code": {"test"}, "state": {form.Get("state")}, "client_id": {"issued-client"}}.Encode()
	response, err = http.Get(callback.String())
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("callback status=%d", response.StatusCode)
	}
	result, ok := manager.Status(status.ID)
	if !ok || result.State != "connected" || result.URL != "" || len(result.Models) != 1 || result.Models[0].Slug != "gpt-6.1-sol" {
		t.Fatalf("login result=%+v", result)
	}
	_, credential, err := manager.store.registration(context.Background())
	if err != nil || credential.Subject != "user" || credential.ClientID != "issued-client" {
		t.Fatal("verified account was not saved")
	}
	for _, change := range []string{"nonce", "aud", "iss", "exp"} {
		claims := map[string]any{"iss": Issuer, "aud": "issued-client", "nonce": nonce, "sub": "user", "exp": time.Now().Add(time.Hour).Unix()}
		claims[change] = "wrong"
		if change == "exp" {
			claims[change] = time.Now().Add(-time.Hour).Unix()
		}
		if _, _, err := manager.store.verifyIdentity(context.Background(), signedToken(t, key, claims), "issued-client", nonce, Issuer+"/jwks"); err == nil {
			t.Fatalf("invalid %s accepted", change)
		}
	}
	forged := signedToken(t, key, map[string]any{"iss": Issuer, "aud": "issued-client", "nonce": nonce, "sub": "user", "exp": time.Now().Add(time.Hour).Unix()})
	parts := strings.Split(forged, ".")
	payload, _ := json.Marshal(map[string]any{"iss": Issuer, "aud": "issued-client", "nonce": nonce, "sub": "attacker", "exp": time.Now().Add(time.Hour).Unix()})
	parts[1] = base64.RawURLEncoding.EncodeToString(payload)
	if _, _, err := manager.store.verifyIdentity(context.Background(), strings.Join(parts, "."), "issued-client", nonce, Issuer+"/jwks"); err == nil {
		t.Fatal("forged identity accepted")
	}
	returning, err := manager.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	returnURL, _ := url.Parse(returning.URL)
	if returnURL.Query().Get("client_id") != "issued-client" || returnURL.Query().Get("agent_name_hint") != "" || returnURL.Query().Get("id_token_hint") != "" {
		t.Fatal("returning login re-registered the client")
	}
}
