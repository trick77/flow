package auth

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

// fakeProvider is an OIDC provider on httptest: discovery, JWKS and a token endpoint that checks the client's
// credentials and the PKCE verifier and redeems each code once, issuing RS256 ID tokens signed with the standard
// library (no JOSE dependency in Flow, tests included).
type fakeProvider struct {
	srv          *httptest.Server
	key          *rsa.PrivateKey
	clientID     string
	clientSecret string

	mu sync.Mutex
	// identity is what the next authorization signs in as: sub, name, groups, ...
	identity map[string]any
	codes    map[string]*grant
	// delay holds every token request this long (or until the client gives up or the test ends).
	delay time.Duration
	stop  chan struct{}
}

type grant struct {
	nonce, challenge, redirectURI string
	identity                      map[string]any
	used                          bool
}

const testKeyID = "k1"

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeProvider{key: key, clientID: "flow", clientSecret: "client-secret", codes: map[string]*grant{},
		stop:     make(chan struct{}),
		identity: map[string]any{"sub": "owner-sub", "name": "Owner Name", "preferred_username": "owner"}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"issuer":                                f.srv.URL,
			"authorization_endpoint":                f.srv.URL + "/authorize",
			"token_endpoint":                        f.srv.URL + "/token",
			"jwks_uri":                              f.srv.URL + "/jwks",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": testKeyID, "use": "sig", "alg": "RS256",
			"n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("POST /token", f.token)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	// Runs first (cleanups are LIFO): a held token request ends before Close waits for it.
	t.Cleanup(func() { close(f.stop) })
	return f
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// authorize plays the provider's login page: it reads the authorization request Flow redirected to and returns the
// code it would send back, bound to the request's nonce, PKCE challenge and the current identity.
func (f *fakeProvider) authorize(t *testing.T, location string) (code, state string) {
	t.Helper()
	u, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Path != "/authorize" || q.Get("client_id") != f.clientID || q.Get("response_type") != "code" ||
		q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" ||
		q.Get("nonce") == "" {
		t.Fatalf("authorization request %s", location)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	code = b64([]byte(time.Now().String() + q.Get("state")))
	f.codes[code] = &grant{nonce: q.Get("nonce"), challenge: q.Get("code_challenge"),
		redirectURI: q.Get("redirect_uri"), identity: f.identity}
	return code, q.Get("state")
}

func (f *fakeProvider) token(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	delay := f.delay
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		case <-f.stop:
			return
		}
	}
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostFormValue("client_id"), r.PostFormValue("client_secret")
	}
	if id != f.clientID || secret != f.clientSecret {
		http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	g := f.codes[r.PostFormValue("code")]
	valid := g != nil && !g.used && r.PostFormValue("grant_type") == "authorization_code" &&
		r.PostFormValue("redirect_uri") == g.redirectURI
	if g != nil {
		g.used = true // single use, whatever the outcome
	}
	f.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostFormValue("code_verifier")))
	if !valid || b64(sum[:]) != g.challenge {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		return
	}
	claims := map[string]any{"iss": f.srv.URL, "aud": f.clientID, "nonce": g.nonce,
		"iat": time.Now().Unix(), "exp": time.Now().Add(5 * time.Minute).Unix()}
	for k, v := range g.identity {
		claims[k] = v
	}
	writeJSON(w, map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 300,
		"id_token": f.sign(claims)})
}

// sign is a compact RS256 JWS over claims.
func (f *fakeProvider) sign(claims map[string]any) string {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": testKeyID, "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	input := b64(header) + "." + b64(payload)
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		panic(err)
	}
	return input + "." + b64(sig)
}

func (f *fakeProvider) signInAs(identity map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.identity = identity
}
