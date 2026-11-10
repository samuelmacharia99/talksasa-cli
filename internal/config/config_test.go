package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TALKSASA_CONFIG_DIR", dir)
	t.Setenv("TALKSASA_NO_KEYRING", "1")
	return dir
}

func TestTokensAreKeptPerAddressInAPrivateFile(t *testing.T) {
	dir := isolate(t)

	if _, _, err := LoadToken("https://a.example/api/v1"); !errors.Is(err, ErrNoToken) {
		t.Fatalf("empty store: %v", err)
	}

	source, path, err := StoreToken("https://a.example/api/v1", "tsk_a")
	if err != nil || source != SourceFile || path != filepath.Join(dir, "credentials.json") {
		t.Fatalf("store: %s %s %v", source, path, err)
	}
	if _, _, err := StoreToken("http://localhost:8000/api/v1", "tsk_local"); err != nil {
		t.Fatal(err)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("credentials.json mode %v (%v)", info.Mode().Perm(), err)
		}
	}

	token, _, err := LoadToken("https://a.example/api/v1")
	if err != nil || token != "tsk_a" {
		t.Fatalf("load: %q %v", token, err)
	}

	removed, err := DeleteToken("https://a.example/api/v1")
	if err != nil || !removed {
		t.Fatalf("delete: %v %v", removed, err)
	}
	if _, _, err := LoadToken("https://a.example/api/v1"); !errors.Is(err, ErrNoToken) {
		t.Fatalf("after delete: %v", err)
	}
	if token, _, _ := LoadToken("http://localhost:8000/api/v1"); token != "tsk_local" {
		t.Fatal("deleting one address removed another's token")
	}
}

func TestATokenFileOthersCanReadIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	dir := isolate(t)
	path := filepath.Join(dir, "credentials.json")
	if err := os.WriteFile(path, []byte(`{"https://a.example/api/v1":"tsk_a"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadToken("https://a.example/api/v1"); err == nil {
		t.Fatal("a world-readable token file was used")
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	isolate(t)
	if s, err := Load(); err != nil || s.APIURL != "" {
		t.Fatalf("empty: %+v %v", s, err)
	}
	if err := Save(Settings{APIURL: "https://x.example/api/v1"}); err != nil {
		t.Fatal(err)
	}
	if s, err := Load(); err != nil || s.APIURL != "https://x.example/api/v1" {
		t.Fatalf("loaded %+v %v", s, err)
	}
}
