package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v0.1.1", "v0.1.0", 1},
		{"v0.2.0", "v0.10.0", -1},
		{"v1.0.0", "v1.0.0", 0},
		{"v1.0.0", "v1.0.0-rc.1", 1},
		{"v1.0.0-rc.2", "v1.0.0-rc.1", 1},
		{"v1.0.0-rc.10", "v1.0.0-rc.9", 1},
		{"v1.0.0-rc.1.1", "v1.0.0-rc.1", 1},
		{"v1.0.0-rc.a", "v1.0.0-rc.2", 1},
		{"v1.0.0+meta", "v1.0.0", 0},
	}
	for _, c := range cases {
		a, okA := parse(c.a)
		b, okB := parse(c.b)
		if !okA || !okB {
			t.Fatalf("parse %q/%q failed", c.a, c.b)
		}
		if got := compare(a, b); got != c.want {
			t.Errorf("compare(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	for _, bad := range []string{"dev", "1.0.0", "v1.0", "v1.x.0"} {
		if _, ok := parse(bad); ok {
			t.Errorf("parse(%q) should fail", bad)
		}
	}
	// A `go install` pseudo-version sorts before the release it follows.
	pseudo, _ := parse("v0.0.0-20261021035150-ff758e370ce5+dirty")
	if rel, _ := parse("v0.1.1"); compare(rel, pseudo) != 1 {
		t.Error("pseudo-version should be older than v0.1.1")
	}
}

func TestLatestFollowsRedirectTarget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok/releases/latest":
			http.Redirect(w, r, "/ok/releases/tag/v0.3.0", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	t.Setenv(EnvReleasesURL, srv.URL+"/ok/releases/latest")
	if tag, err := Latest(context.Background()); err != nil || tag != "v0.3.0" {
		t.Errorf("Latest = %q, %v", tag, err)
	}
	t.Setenv(EnvReleasesURL, srv.URL+"/missing")
	if _, err := Latest(context.Background()); err == nil {
		t.Error("a non-redirect response must be an error")
	}
}
