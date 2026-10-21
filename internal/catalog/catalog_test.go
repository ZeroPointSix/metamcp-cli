package catalog

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestVariants(t *testing.T) {
	cases := []struct {
		name string
		want []string
	}{
		{"VPS__run_script", []string{"VPS__run-script"}},
		{"railway__list_projects", []string{"railway__list-projects"}},
		{"VPS__fs-write", []string{"VPS__fs_write"}},
		{"Windows_MCP__click", []string{"Windows-MCP__click"}},
		{"github__get_me", []string{"github__get-me"}},
		{"VPS__exec", nil},
		{"nosep", nil},
	}
	for _, c := range cases {
		if got := Variants(c.name); !slices.Equal(got, c.want) {
			t.Errorf("Variants(%q) = %v, want %v", c.name, got, c.want)
		}
	}
	got := Variants("a__b_c-d")
	if len(got) != 3 || slices.Contains(got, "a__b_c-d") || !slices.Contains(got, "a__b-c_d") {
		t.Errorf("mixed separators: %v", got)
	}
	long := Variants("s__" + strings.Repeat("a_", 10) + "z")
	if len(long) != 1 || long[0] != "s__"+strings.Repeat("a-", 10)+"z" {
		t.Errorf("many separators should only try uniform spellings: %v", long)
	}
	if n := len(Variants("x_y_z__a_b_c_d_e_f")); n > maxVariants {
		t.Errorf("%d variants, cap is %d", n, maxVariants)
	}
}

func TestNormalizeAndCallHint(t *testing.T) {
	for in, want := range map[string]string{
		"VPS__exec":            "VPS__exec",
		"VPS.exec":             "VPS__exec",
		"tools.VPS.run-script": "VPS__run-script",
		"tools.VPS__exec":      "VPS__exec",
		" github__get_me ":     "github__get_me",
		"plain":                "plain",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"VPS__run-script":    `tools.VPS["run-script"](args)`,
		"github__get_me":     `tools.github.get_me(args)`,
		"Windows-MCP__click": `tools["Windows-MCP"].click(args)`,
	} {
		if got := CallHint(in); got != want {
			t.Errorf("CallHint(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestScriptEmbedsArgsAsJSON(t *testing.T) {
	code, err := Script("return args;", map[string]any{"names": []string{`a"b`}})
	if err != nil {
		t.Fatal(err)
	}
	line, body, _ := strings.Cut(code, "\n")
	var got map[string][]string
	if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(line, "const args = "), ";")), &got); err != nil || got["names"][0] != `a"b` || body != "return args;" {
		t.Errorf("code = %q (%v)", code, err)
	}
}
