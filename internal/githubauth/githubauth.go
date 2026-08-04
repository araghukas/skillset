// Package githubauth supplies the credential used for every GitHub call
// skillset makes: the HTTPS git clone/fetch/push, and the REST call that
// opens a pull request.
//
// Two modes are supported. A static token (a PAT, or a Gitea token in local
// development) is the simple case and never changes. A GitHub App mints a
// short-lived installation access token instead: the app's private key signs
// a JWT, the JWT is exchanged for a token, and that token expires within the
// hour. Callers therefore ask for the credential per operation rather than
// reading it once at startup - see TokenSource.
//
// App auth is implemented here directly rather than via an SDK. It is one
// signature and one request, and the available Go libraries pull in either a
// full generated API surface or an unrelated HTTP stack for it.
package githubauth

import (
	"bytes"
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
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// TokenSource yields a credential that is valid at the moment it's called.
// A static token returns the same string forever; a GitHub App returns a
// cached installation token, minting a fresh one as expiry approaches.
//
// Implementations are safe for concurrent use.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// Static returns a TokenSource that always yields token. Used for PAT auth
// and for Gitea, which has no GitHub App equivalent.
func Static(token string) TokenSource { return staticSource(token) }

type staticSource string

func (s staticSource) Token(context.Context) (string, error) { return string(s), nil }

// DefaultAPIBaseURL is the GitHub REST API root, used when AppConfig leaves
// APIBaseURL empty.
const DefaultAPIBaseURL = "https://api.github.com"

// jwtLifetime is how long the app JWT used to request an installation token
// is valid for. GitHub rejects anything over 10 minutes; the clock skew
// allowance below eats into that, so this stays comfortably under.
const jwtLifetime = 9 * time.Minute

// jwtBackdate shifts the JWT's issued-at into the past, because GitHub
// rejects a JWT issued in the future and our clock may run slightly ahead of
// theirs.
const jwtBackdate = 60 * time.Second

// tokenRefreshMargin is how far before its stated expiry a cached
// installation token is discarded, so a token doesn't lapse midway through
// the request it was fetched for.
const tokenRefreshMargin = time.Minute

// AppConfig describes a GitHub App installation.
type AppConfig struct {
	// APIBaseURL is the GitHub REST API root, e.g. for GitHub Enterprise.
	// Defaults to DefaultAPIBaseURL when empty.
	APIBaseURL string

	// Issuer identifies the app to GitHub. Either the app's client ID
	// ("Iv23...") or its numeric app ID works: it's only ever the JWT's
	// "iss" claim, so this is deliberately a string and isn't validated as
	// a number.
	Issuer string

	// InstallationID identifies which installation of the app to act as.
	// Distinct from Issuer above: an app has one ID but is installed
	// separately on each account or organization that adopts it.
	InstallationID int64

	// PrivateKeyPEM is the app's private key, in the PKCS#1 PEM form GitHub
	// hands out ("RSA PRIVATE KEY") or PKCS#8 ("PRIVATE KEY").
	PrivateKeyPEM []byte
}

// App mints installation access tokens for a single GitHub App
// installation, caching each one until shortly before it expires.
type App struct {
	baseURL        string
	issuer         string
	installationID int64
	key            *rsa.PrivateKey
	httpClient     *http.Client

	// now is time.Now in production; tests substitute a controllable clock
	// to exercise cache expiry without sleeping.
	now func() time.Time

	mu          sync.Mutex
	cached      string
	cachedUntil time.Time
}

// NewApp returns an App for cfg, parsing the private key up front so a
// malformed key fails at startup rather than on the first push.
func NewApp(cfg AppConfig) (*App, error) {
	if cfg.Issuer == "" {
		return nil, fmt.Errorf("githubauth: app issuer (app ID or client ID) is required")
	}
	if cfg.InstallationID == 0 {
		return nil, fmt.Errorf("githubauth: app installation ID is required")
	}

	key, err := parsePrivateKey(cfg.PrivateKeyPEM)
	if err != nil {
		return nil, err
	}

	baseURL := cfg.APIBaseURL
	if baseURL == "" {
		baseURL = DefaultAPIBaseURL
	}

	return &App{
		baseURL:        strings.TrimSuffix(baseURL, "/"),
		issuer:         cfg.Issuer,
		installationID: cfg.InstallationID,
		key:            key,
		httpClient:     http.DefaultClient,
		now:            time.Now,
	}, nil
}

// parsePrivateKey accepts both PEM encodings GitHub apps turn up in:
// PKCS#1, which is what the GitHub UI downloads, and PKCS#8, which is what
// you get after a round trip through most key-management tooling.
func parsePrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("githubauth: private key is not PEM-encoded")
	}

	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}

	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("githubauth: parsing private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("githubauth: private key is %T, want an RSA key", parsed)
	}
	return key, nil
}

// Token returns a valid installation access token, reusing the cached one
// until it's close enough to expiry to be worth replacing.
func (a *App) Token(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.cached != "" && a.now().Before(a.cachedUntil) {
		return a.cached, nil
	}

	token, expiresAt, err := a.mintToken(ctx)
	if err != nil {
		return "", err
	}

	a.cached = token
	a.cachedUntil = expiresAt.Add(-tokenRefreshMargin)
	return token, nil
}

// mintToken exchanges a freshly signed app JWT for an installation access
// token. Callers must hold a.mu.
func (a *App) mintToken(ctx context.Context) (string, time.Time, error) {
	assertion, err := a.signJWT()
	if err != nil {
		return "", time.Time{}, err
	}

	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", a.baseURL, a.installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(nil))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("githubauth: building token request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+assertion)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("githubauth: requesting installation token: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("githubauth: reading token response: %w", err)
	}
	if resp.StatusCode != http.StatusCreated {
		return "", time.Time{}, fmt.Errorf("githubauth: requesting installation token: %s: %s", resp.Status, body)
	}

	var parsed struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", time.Time{}, fmt.Errorf("githubauth: decoding token response: %w", err)
	}
	if parsed.Token == "" {
		return "", time.Time{}, fmt.Errorf("githubauth: installation token response contained no token")
	}

	return parsed.Token, parsed.ExpiresAt, nil
}

// signJWT builds the RS256-signed assertion that authenticates us as the app
// itself (rather than as one of its installations).
func (a *App) signJWT() (string, error) {
	issuedAt := a.now().Add(-jwtBackdate).Truncate(time.Second)

	header, err := json.Marshal(struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}{Alg: "RS256", Typ: "JWT"})
	if err != nil {
		return "", fmt.Errorf("githubauth: encoding JWT header: %w", err)
	}

	claims, err := json.Marshal(struct {
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
		Iss string `json:"iss"`
	}{
		Iat: issuedAt.Unix(),
		Exp: issuedAt.Add(jwtLifetime).Unix(),
		Iss: a.issuer,
	})
	if err != nil {
		return "", fmt.Errorf("githubauth: encoding JWT claims: %w", err)
	}

	enc := base64.RawURLEncoding
	signingInput := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)

	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("githubauth: signing JWT: %w", err)
	}

	return signingInput + "." + enc.EncodeToString(signature), nil
}
