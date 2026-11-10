// Package config holds the CLI's settings and where tokens are kept.
//
// Settings live in <user config dir>/talksasa/config.json (TALKSASA_CONFIG_DIR
// overrides the directory). Tokens are kept per API address, in the system
// keychain when there is one, otherwise in credentials.json readable only by
// the user. TALKSASA_TOKEN, when set, wins over both and nothing is stored.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Settings is config.json.
type Settings struct {
	// APIURL is the API base the user chose at login, when not the default.
	APIURL string `json:"api_url,omitempty"`
}

// Dir is where the CLI keeps its files.
func Dir() (string, error) {
	if dir := os.Getenv("TALKSASA_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find the config directory: %w", err)
	}
	return filepath.Join(base, "talksasa"), nil
}

// Load reads config.json; a missing file is empty settings.
func Load() (Settings, error) {
	var s Settings
	dir, err := Dir()
	if err != nil {
		return s, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, fmt.Errorf("%s is not valid JSON: %w", filepath.Join(dir, "config.json"), err)
	}
	return s, nil
}

// Save writes config.json.
func Save(s Settings) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(dir, "config.json", append(raw, '\n'))
}

// writePrivate writes a file only the user can read, replacing it whole so
// a crash never leaves half a file.
func writePrivate(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+name+".*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0o600); err != nil && !isWindows() {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filepath.Join(dir, name))
}
