package githubauth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testKey generates a throwaway RSA key. 2048 bits because GitHub won't
// issue smaller ones, and generation cost is not what these tests measure.
func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func pkcs1PEM(t *testing.T, key *rsa.PrivateKey) []byte {
	t.Helper()
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
}

func pkcs8PEM(t *testing.T, key *rsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// tokenServer stands in for GitHub's installation-token endpoint. It records
// every assertion it was handed and hands back a distinct token per call, so
// tests can tell a cache hit from a refresh.
type tokenServer struct {
	*httptest.Server
	calls      int
	assertions []string
	paths      []string
	expiresIn  time.Duration
}

func newTokenServer(t *testing.T, expiresIn time.Duration) *tokenServer {
	t.Helper()
	ts := &tokenServer{expiresIn: expiresIn}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts.calls++
		ts.paths = append(ts.paths, r.URL.Path)
		ts.assertions = append(ts.assertions, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))

		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      fmt.Sprintf("ghs_token_%d", ts.calls),
			"expires_at": time.Now().Add(ts.expiresIn).Format(time.RFC3339),
		})
	}))
	t.Cleanup(ts.Close)
	return ts
}

func newTestApp(t *testing.T, srv *tokenServer, key *rsa.PrivateKey) *App {
	t.Helper()
	app, err := NewApp(AppConfig{
		APIBaseURL:     srv.URL,
		Issuer:         "Iv23testclientid",
		InstallationID: 42,
		PrivateKeyPEM:  pkcs1PEM(t, key),
	})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func TestStaticAlwaysReturnsTheSameToken(t *testing.T) {
	src := Static("pat-abc")
	for range 2 {
		got, err := src.Token(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got != "pat-abc" {
			t.Errorf("expected pat-abc, got %s", got)
		}
	}
}

func TestAppExchangesSignedJWTForInstallationToken(t *testing.T) {
	key := testKey(t)
	srv := newTokenServer(t, time.Hour)
	app := newTestApp(t, srv, key)

	got, err := app.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "ghs_token_1" {
		t.Errorf("expected the token from the response body, got %s", got)
	}

	if want := "/app/installations/42/access_tokens"; srv.paths[0] != want {
		t.Errorf("expected path %s, got %s", want, srv.paths[0])
	}

	// The assertion must be a well-formed RS256 JWT, issued by the app and
	// verifiable with its public key - that signature is the whole basis on
	// which GitHub decides to hand out an installation token.
	parts := strings.Split(srv.assertions[0], ".")
	if len(parts) != 3 {
		t.Fatalf("expected a three-part JWT, got %d parts", len(parts))
	}

	var header struct{ Alg, Typ string }
	decodeJWTPart(t, parts[0], &header)
	if header.Alg != "RS256" || header.Typ != "JWT" {
		t.Errorf("expected RS256/JWT header, got %s/%s", header.Alg, header.Typ)
	}

	var claims struct {
		Iat int64
		Exp int64
		Iss string
	}
	decodeJWTPart(t, parts[1], &claims)
	if claims.Iss != "Iv23testclientid" {
		t.Errorf("expected the configured issuer, got %s", claims.Iss)
	}
	if now := time.Now().Unix(); claims.Iat > now {
		t.Errorf("iat %d is in the future (now %d); GitHub rejects those", claims.Iat, now)
	}
	if lifetime := claims.Exp - claims.Iat; lifetime > 600 {
		t.Errorf("JWT lifetime %ds exceeds GitHub's 10 minute maximum", lifetime)
	}

	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Errorf("JWT signature does not verify against the app's key: %v", err)
	}
}

func decodeJWTPart(t *testing.T, part string, into any) {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(part)
	if err != nil {
		t.Fatalf("decoding JWT part: %v", err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("unmarshalling JWT part: %v", err)
	}
}

func TestAppCachesTokenUntilItNearsExpiry(t *testing.T) {
	srv := newTokenServer(t, time.Hour)
	app := newTestApp(t, srv, testKey(t))

	clock := time.Now()
	app.now = func() time.Time { return clock }

	first, err := app.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Well inside the hour: the cached token should be reused rather than
	// costing a round trip per git push.
	clock = clock.Add(30 * time.Minute)
	second, err := app.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Errorf("expected the cached token %s, got %s", first, second)
	}
	if srv.calls != 1 {
		t.Errorf("expected 1 token request while cached, got %d", srv.calls)
	}

	// Past expiry: a long-lived registry must mint a new one rather than
	// keep presenting a dead credential.
	clock = clock.Add(31 * time.Minute)
	third, err := app.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if third == first {
		t.Error("expected a freshly minted token after expiry, got the cached one")
	}
	if srv.calls != 2 {
		t.Errorf("expected 2 token requests after expiry, got %d", srv.calls)
	}
}

func TestAppRefreshesBeforeStatedExpiry(t *testing.T) {
	// A token that is still technically valid, but only just, is worse than
	// no token: it can lapse midway through the push it was fetched for.
	srv := newTokenServer(t, time.Hour)
	app := newTestApp(t, srv, testKey(t))

	clock := time.Now()
	app.now = func() time.Time { return clock }

	if _, err := app.Token(context.Background()); err != nil {
		t.Fatal(err)
	}

	clock = clock.Add(time.Hour - 30*time.Second)
	if _, err := app.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if srv.calls != 2 {
		t.Errorf("expected a refresh inside the %s margin, got %d requests", tokenRefreshMargin, srv.calls)
	}
}

func TestAppSurfacesTokenEndpointErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Integration not found"}`))
	}))
	defer srv.Close()

	app, err := NewApp(AppConfig{
		APIBaseURL:     srv.URL,
		Issuer:         "123456",
		InstallationID: 7,
		PrivateKeyPEM:  pkcs1PEM(t, testKey(t)),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = app.Token(context.Background())
	if err == nil {
		t.Fatal("expected an error from a 401 response")
	}
	if !strings.Contains(err.Error(), "Integration not found") {
		t.Errorf("expected GitHub's message in the error, got %v", err)
	}
}

func TestNewAppAcceptsBothPEMEncodings(t *testing.T) {
	// GitHub's UI hands out PKCS#1; a round trip through most key management
	// tooling produces PKCS#8. Both turn up in practice.
	key := testKey(t)
	for name, keyPEM := range map[string][]byte{
		"pkcs1": pkcs1PEM(t, key),
		"pkcs8": pkcs8PEM(t, key),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewApp(AppConfig{Issuer: "1", InstallationID: 1, PrivateKeyPEM: keyPEM}); err != nil {
				t.Errorf("expected %s to parse, got %v", name, err)
			}
		})
	}
}

func TestNewAppRejectsIncompleteConfig(t *testing.T) {
	keyPEM := pkcs1PEM(t, testKey(t))

	tests := map[string]AppConfig{
		"no issuer":          {InstallationID: 1, PrivateKeyPEM: keyPEM},
		"no installation ID": {Issuer: "1", PrivateKeyPEM: keyPEM},
		"no key":             {Issuer: "1", InstallationID: 1},
		"garbage key":        {Issuer: "1", InstallationID: 1, PrivateKeyPEM: []byte("not a pem file")},
	}
	for name, cfg := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := NewApp(cfg); err == nil {
				t.Error("expected an error, got nil")
			}
		})
	}
}

func TestNewAppDefaultsToPublicGitHub(t *testing.T) {
	app, err := NewApp(AppConfig{Issuer: "1", InstallationID: 1, PrivateKeyPEM: pkcs1PEM(t, testKey(t))})
	if err != nil {
		t.Fatal(err)
	}
	if app.baseURL != DefaultAPIBaseURL {
		t.Errorf("expected %s, got %s", DefaultAPIBaseURL, app.baseURL)
	}
}
