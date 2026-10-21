// Package update looks up the newest mcli release on GitHub.
//
// It reads the redirect of https://github.com/<repo>/releases/latest instead
// of calling the REST API, which is rate limited to 60 unauthenticated
// requests per hour and IP — shared CI runners hit that quickly.
package update

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/ZeroPointSix/metamcp-cli/internal/output"
	"github.com/ZeroPointSix/metamcp-cli/internal/version"
)

const (
	// EnvReleasesURL overrides the "latest release" URL (mirrors, tests).
	EnvReleasesURL = "MCLI_RELEASES_URL"
	// EnvNoUpdateCheck disables the check inside `mcli doctor`.
	EnvNoUpdateCheck = "MCLI_NO_UPDATE_CHECK"

	defaultReleasesURL = "https://github.com/ZeroPointSix/metamcp-cli/releases/latest"

	// UpgradeHint tells how to upgrade: the install scripts are idempotent.
	UpgradeHint = "Re-run the install script to upgrade: `curl -fsSL https://raw.githubusercontent.com/ZeroPointSix/metamcp-cli/main/install.sh | sh` (Windows: `irm https://raw.githubusercontent.com/ZeroPointSix/metamcp-cli/main/install.ps1 | iex`)."
)

// Check compares the running version with the latest release.
type Check struct {
	Current string `json:"current"`
	Latest  string `json:"latest"`
	// UpdateAvailable is nil when the running binary is not a release build.
	UpdateAvailable *bool  `json:"update_available"`
	Upgrade         string `json:"upgrade,omitempty"`
}

// Disabled reports whether MCLI_NO_UPDATE_CHECK turns off implicit checks.
func Disabled() bool {
	v, err := strconv.ParseBool(os.Getenv(EnvNoUpdateCheck))
	return err == nil && v
}

// Latest returns the newest release tag, e.g. "v0.1.1".
func Latest(ctx context.Context) (string, error) {
	u := os.Getenv(EnvReleasesURL)
	if u == "" {
		u = defaultReleasesURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
	if err != nil {
		return "", output.Newf(output.CodeInternal, "release lookup: %v", err)
	}
	req.Header.Set("User-Agent", version.UserAgent())
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		e := output.Newf(output.CodeNetwork, "release lookup failed: %v", err)
		e.Hint = "Check network access to github.com, or set " + EnvReleasesURL + " to a mirror."
		return "", e
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	tag := loc[strings.LastIndex(loc, "/")+1:]
	if resp.StatusCode < 300 || resp.StatusCode >= 400 || !strings.HasPrefix(tag, "v") {
		return "", output.Newf(output.CodeProtocol, "release lookup: unexpected response (HTTP %d, Location %q)", resp.StatusCode, loc)
	}
	return tag, nil
}

// Run looks up the latest release and compares it with the running version.
func Run(ctx context.Context) (*Check, error) {
	latest, err := Latest(ctx)
	if err != nil {
		return nil, err
	}
	c := &Check{Current: version.Version, Latest: latest}
	if cur, ok := parse(version.Version); ok {
		if lat, ok := parse(latest); ok {
			newer := compare(lat, cur) > 0
			c.UpdateAvailable = &newer
			if newer {
				c.Upgrade = UpgradeHint
			}
		}
	}
	return c, nil
}

// semver is major.minor.patch plus an optional pre-release suffix.
type semver struct {
	n   [3]int
	pre string
}

func parse(v string) (semver, bool) {
	var s semver
	v, ok := strings.CutPrefix(v, "v")
	if !ok {
		return s, false
	}
	v, _, _ = strings.Cut(v, "+")
	v, s.pre, _ = strings.Cut(v, "-")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return s, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return s, false
		}
		s.n[i] = n
	}
	return s, true
}

// compare orders versions; a release sorts after its pre-releases.
// Pre-release suffixes compare as strings, which is enough for -rc.N.
func compare(a, b semver) int {
	for i := range a.n {
		if a.n[i] != b.n[i] {
			if a.n[i] > b.n[i] {
				return 1
			}
			return -1
		}
	}
	switch {
	case a.pre == b.pre:
		return 0
	case a.pre == "":
		return 1
	case b.pre == "":
		return -1
	}
	return strings.Compare(a.pre, b.pre)
}
