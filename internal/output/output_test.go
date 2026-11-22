package output

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSpillDataIsLossless(t *testing.T) {
	dir := t.TempDir()
	r := Success(json.RawMessage(`{"z":1,"id":12345678901234567890,"html":"<a&b>"}`))
	if err := r.SpillData("", dir); err != nil {
		t.Fatal(err)
	}
	if r.Data != nil || r.DataFile == nil {
		t.Fatalf("result = %+v", r)
	}
	raw, err := os.ReadFile(r.DataFile.Path)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"z\": 1,\n  \"id\": 12345678901234567890,\n  \"html\": \"<a&b>\"\n}\n"
	if string(raw) != want {
		t.Errorf("file:\n%s\nwant:\n%s", raw, want)
	}
	if r.DataFile.Bytes != len(raw) {
		t.Errorf("bytes = %d, file has %d", r.DataFile.Bytes, len(raw))
	}
	if m, _ := filepath.Match(SpillPattern, filepath.Base(r.DataFile.Path)); !m {
		t.Errorf("%s does not match SpillPattern", r.DataFile.Path)
	}
}

func TestSpillDataCreatesOutParent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "out.json")
	r := Success(json.RawMessage(`1`))
	if err := r.SpillData(path, ""); err != nil {
		t.Fatal(err)
	}
	if r.DataFile.Path != path {
		t.Errorf("path = %s", r.DataFile.Path)
	}
}
