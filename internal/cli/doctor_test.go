package cli

import (
	"testing"

	"github.com/ZeroPointSix/metamcp-cli/internal/output"
)

func TestClassifyProbe(t *testing.T) {
	reauth := `{"error":"Upstream requires re-authorization","error_code":"AUTH_REAUTHORIZATION_REQUIRED","recovery":"Authorize again","authorization_url":"https://x/auth"}`
	cases := []struct {
		code, message, want string
	}{
		{"E_RUNTIME", reauth, probeAuthRequired},
		{"E_RUNTIME", `{"error":"bad token","error_class":"auth_invalid"}`, probeAuthRequired},
		{output.CodeToolNotFound, `{"error":"Unknown tool: a__b"}`, probeNotFound},
		{"E_RUNTIME", `{"error":"Unknown tool: a__b","error_code":"UNKNOWN_TOOL"}`, probeNotFound},
		{"E_RUNTIME", `{"error":"Unknown tool: a__b","error_class":"unknown_tool"}`, probeNotFound},
		{"E_RUNTIME", "Unknown tool: a__b", probeNotFound},
		{"E_CHILD_TIMEOUT", "Child tool call timed out.", probeTimeout},
		{"E_RUNTIME", `{"error":"timed out","error_class":"timeout"}`, probeTimeout},
		{"E_INVALID_ARGUMENT", "command is required", probeError},
	}
	for _, c := range cases {
		if got := classifyProbe(c.code, c.message); got.Status != c.want {
			t.Errorf("classifyProbe(%s, %s) = %s, want %s", c.code, c.message, got.Status, c.want)
		}
	}
	r := classifyProbe("E_RUNTIME", reauth)
	if r.Message != "Upstream requires re-authorization" || r.NextAction != "Authorize again" || r.AuthorizationURL != "https://x/auth" {
		t.Errorf("contract fields not extracted: %+v", r)
	}
}

func TestParseProbe(t *testing.T) {
	p, err := parseProbe(`VPS.exec={"command":"true"}`)
	if err != nil || p.Name != "VPS__exec" || p.Args["command"] != "true" {
		t.Errorf("parseProbe = %+v, %v", p, err)
	}
	if p, err := parseProbe("github__get_me"); err != nil || len(p.Args) != 0 {
		t.Errorf("no args: %+v, %v", p, err)
	}
	for _, bad := range []string{"=", "x=[1]", "x=null", "x={"} {
		if _, err := parseProbe(bad); err == nil {
			t.Errorf("parseProbe(%q) should fail", bad)
		}
	}
}

func TestSimilarNames(t *testing.T) {
	got := similarNames("windows_mcp", []string{"Windows-MCP", "VPS", "windows_mcp"})
	if len(got) != 1 || got[0] != "Windows-MCP" {
		t.Errorf("similarNames = %v", got)
	}
}
