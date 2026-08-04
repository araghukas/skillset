package githubauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clearEnv unsets everything LoadFromEnv reads, so a developer's own
// GITHUB_TOKEN doesn't quietly change what these tests exercise. t.Setenv
// restores the previous values afterwards.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"GITHUB_AUTH_MODE",
		"GITHUB_TOKEN",
		"GITHUB_APP_ID",
		"GITHUB_APP_INSTALLATION_ID",
		"GITHUB_APP_PRIVATE_KEY_PATH",
		"GITHUB_API_BASE_URL",
	} {
		t.Setenv(key, "")
	}
}

func writeTestKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "private-key.pem")
	if err := os.WriteFile(path, pkcs1PEM(t, key), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadFromEnvDefaultsToTokenMode(t *testing.T) {
	clearEnv(t)
	t.Setenv("GITHUB_TOKEN", "pat-abc")

	src, mode, err := LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if mode != ModeToken {
		t.Errorf("expected mode %s, got %s", ModeToken, mode)
	}
	got, err := src.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "pat-abc" {
		t.Errorf("expected pat-abc, got %s", got)
	}
}

func TestLoadFromEnvWithoutCredentialsIsNotAnError(t *testing.T) {
	clearEnv(t)
	// An unauthenticated public clone is a supported deployment, so an
	// absent token has to mean "no credential", not "misconfigured".
	src, mode, err := LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if src != nil {
		t.Errorf("expected a nil TokenSource, got %#v", src)
	}
	if mode != ModeNone {
		t.Errorf("expected mode %s, got %s", ModeNone, mode)
	}
}

func TestLoadFromEnvExplicitNoneIgnoresAToken(t *testing.T) {
	clearEnv(t)
	t.Setenv("GITHUB_AUTH_MODE", string(ModeNone))
	t.Setenv("GITHUB_TOKEN", "pat-abc")

	src, mode, err := LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if src != nil || mode != ModeNone {
		t.Errorf("expected no credential, got %#v in mode %s", src, mode)
	}
}

func TestLoadFromEnvBuildsAnApp(t *testing.T) {
	clearEnv(t)
	t.Setenv("GITHUB_AUTH_MODE", string(ModeGitHubApp))
	t.Setenv("GITHUB_APP_ID", "Iv23abc")
	t.Setenv("GITHUB_APP_INSTALLATION_ID", "987")
	t.Setenv("GITHUB_APP_PRIVATE_KEY_PATH", writeTestKey(t))

	src, mode, err := LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if mode != ModeGitHubApp {
		t.Errorf("expected mode %s, got %s", ModeGitHubApp, mode)
	}
	app, ok := src.(*App)
	if !ok {
		t.Fatalf("expected an *App, got %T", src)
	}
	if app.issuer != "Iv23abc" || app.installationID != 987 {
		t.Errorf("expected issuer Iv23abc / installation 987, got %s / %d", app.issuer, app.installationID)
	}
}

func TestLoadFromEnvRejectsIncompleteAppConfig(t *testing.T) {
	clearEnv(t)
	// Unlike a missing token, a half-configured app can only be a mistake:
	// failing at startup beats a pod that comes up and can't push.
	keyPath := writeTestKey(t)

	tests := map[string]map[string]string{
		"no app ID":          {"GITHUB_APP_INSTALLATION_ID": "1", "GITHUB_APP_PRIVATE_KEY_PATH": keyPath},
		"no installation ID": {"GITHUB_APP_ID": "1", "GITHUB_APP_PRIVATE_KEY_PATH": keyPath},
		"no key path":        {"GITHUB_APP_ID": "1", "GITHUB_APP_INSTALLATION_ID": "1"},
		"nothing at all":     {},
	}
	for name, env := range tests {
		t.Run(name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("GITHUB_AUTH_MODE", string(ModeGitHubApp))
			for k, v := range env {
				t.Setenv(k, v)
			}
			if _, _, err := LoadFromEnv(); err == nil {
				t.Error("expected an error, got nil")
			}
		})
	}
}

func TestLoadFromEnvRejectsBadAppValues(t *testing.T) {
	clearEnv(t)
	t.Run("non-numeric installation ID", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("GITHUB_AUTH_MODE", string(ModeGitHubApp))
		t.Setenv("GITHUB_APP_ID", "1")
		t.Setenv("GITHUB_APP_INSTALLATION_ID", "not-a-number")
		t.Setenv("GITHUB_APP_PRIVATE_KEY_PATH", writeTestKey(t))

		if _, _, err := LoadFromEnv(); err == nil {
			t.Error("expected an error, got nil")
		}
	})

	t.Run("unreadable key path", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("GITHUB_AUTH_MODE", string(ModeGitHubApp))
		t.Setenv("GITHUB_APP_ID", "1")
		t.Setenv("GITHUB_APP_INSTALLATION_ID", "1")
		t.Setenv("GITHUB_APP_PRIVATE_KEY_PATH", filepath.Join(t.TempDir(), "absent.pem"))

		if _, _, err := LoadFromEnv(); err == nil {
			t.Error("expected an error, got nil")
		}
	})
}

func TestLoadFromEnvRejectsUnknownMode(t *testing.T) {
	clearEnv(t)
	t.Setenv("GITHUB_AUTH_MODE", "oauth")

	_, _, err := LoadFromEnv()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "oauth") {
		t.Errorf("expected the offending value in the error, got %v", err)
	}
}
