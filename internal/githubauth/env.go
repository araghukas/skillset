package githubauth

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Mode names the credential scheme a process was configured with.
type Mode string

const (
	// ModeNone is unauthenticated access - a public clone, and no ability
	// to push or open pull requests.
	ModeNone Mode = "none"

	// ModeToken is a static token: a GitHub PAT, or a Gitea token locally.
	ModeToken Mode = "token"

	// ModeGitHubApp is GitHub App installation auth.
	ModeGitHubApp Mode = "githubApp"
)

// LoadFromEnv builds the TokenSource for this process from the environment.
// Both skillsd-init (which clones) and skillsd-registry (which clones,
// pushes, and opens pull requests) read the same variables, so a deployment
// configures auth once regardless of which component consumes it:
//
//	GITHUB_AUTH_MODE             none | token | githubApp (default token)
//	GITHUB_TOKEN                 mode token
//	GITHUB_APP_ID                mode githubApp; app ID or client ID
//	GITHUB_APP_INSTALLATION_ID   mode githubApp
//	GITHUB_APP_PRIVATE_KEY_PATH  mode githubApp; path to the mounted PEM
//	GITHUB_API_BASE_URL          token exchange host, for GitHub Enterprise
//
// A nil TokenSource and a nil error mean "no credential configured", which
// is a legitimate configuration: callers decide whether that's an
// unauthenticated public clone or a reason to disable a write path.
//
// Mode token with no GITHUB_TOKEN set is treated as ModeNone rather than an
// error, because that's the default mode and an absent token is how a public
// repo is configured. An incomplete githubApp config, by contrast, is always
// an error: it can only be a mistake.
func LoadFromEnv() (TokenSource, Mode, error) {
	mode := Mode(strings.TrimSpace(os.Getenv("GITHUB_AUTH_MODE")))
	if mode == "" {
		mode = ModeToken
	}

	switch mode {
	case ModeNone:
		return nil, ModeNone, nil

	case ModeToken:
		token := os.Getenv("GITHUB_TOKEN")
		if token == "" {
			return nil, ModeNone, nil
		}
		return Static(token), ModeToken, nil

	case ModeGitHubApp:
		app, err := appFromEnv()
		if err != nil {
			return nil, "", err
		}
		return app, ModeGitHubApp, nil

	default:
		return nil, "", fmt.Errorf("githubauth: unknown GITHUB_AUTH_MODE %q (want %s, %s, or %s)",
			mode, ModeNone, ModeToken, ModeGitHubApp)
	}
}

func appFromEnv() (*App, error) {
	issuer := strings.TrimSpace(os.Getenv("GITHUB_APP_ID"))
	rawInstallationID := strings.TrimSpace(os.Getenv("GITHUB_APP_INSTALLATION_ID"))
	keyPath := strings.TrimSpace(os.Getenv("GITHUB_APP_PRIVATE_KEY_PATH"))

	var missing []string
	if issuer == "" {
		missing = append(missing, "GITHUB_APP_ID")
	}
	if rawInstallationID == "" {
		missing = append(missing, "GITHUB_APP_INSTALLATION_ID")
	}
	if keyPath == "" {
		missing = append(missing, "GITHUB_APP_PRIVATE_KEY_PATH")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("githubauth: GITHUB_AUTH_MODE=%s requires %s",
			ModeGitHubApp, strings.Join(missing, ", "))
	}

	installationID, err := strconv.ParseInt(rawInstallationID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("githubauth: parsing GITHUB_APP_INSTALLATION_ID: %w", err)
	}

	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("githubauth: reading GITHUB_APP_PRIVATE_KEY_PATH: %w", err)
	}

	return NewApp(AppConfig{
		APIBaseURL:     os.Getenv("GITHUB_API_BASE_URL"),
		Issuer:         issuer,
		InstallationID: installationID,
		PrivateKeyPEM:  keyPEM,
	})
}
