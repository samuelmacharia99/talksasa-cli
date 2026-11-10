package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/zalando/go-keyring"
)

// keyringService names the CLI's entries in the system keychain.
const keyringService = "talksasa-cli"

// Where a token came from or went to.
const (
	SourceEnv     = "TALKSASA_TOKEN"
	SourceKeyring = "keychain"
	SourceFile    = "file"
)

// ErrNoToken means no token is stored for that API address.
var ErrNoToken = errors.New("no token stored")

// keyringDisabled lets a headless machine, and the tests, keep the system
// keychain out of it.
func keyringDisabled() bool { return os.Getenv("TALKSASA_NO_KEYRING") != "" }

// TokenFromEnv is TALKSASA_TOKEN, the token CI passes.
func TokenFromEnv() string { return os.Getenv("TALKSASA_TOKEN") }

// LoadToken finds the stored token for an API address.
func LoadToken(apiURL string) (token, source string, err error) {
	if !keyringDisabled() {
		token, err := keyring.Get(keyringService, apiURL)
		if err == nil && token != "" {
			return token, SourceKeyring, nil
		}
	}

	tokens, err := readCredentialsFile()
	if err != nil {
		return "", "", err
	}
	if token := tokens[apiURL]; token != "" {
		return token, SourceFile, nil
	}
	return "", "", ErrNoToken
}

// StoreToken keeps a token for an API address: in the keychain when one
// works, otherwise in credentials.json. It says which.
func StoreToken(apiURL, token string) (source string, path string, err error) {
	if !keyringDisabled() {
		if err := keyring.Set(keyringService, apiURL, token); err == nil {
			// A token left in the file from before would shadow nothing,
			// but would sit on disk for no reason.
			_ = removeFromFile(apiURL)
			return SourceKeyring, "", nil
		}
	}

	tokens, err := readCredentialsFile()
	if err != nil {
		return "", "", err
	}
	tokens[apiURL] = token
	if err := writeCredentialsFile(tokens); err != nil {
		return "", "", err
	}
	path, _ = credentialsPath()
	return SourceFile, path, nil
}

// DeleteToken removes the token for an API address from both places. It
// reports whether one was there.
func DeleteToken(apiURL string) (bool, error) {
	removed := false
	if !keyringDisabled() {
		if err := keyring.Delete(keyringService, apiURL); err == nil {
			removed = true
		}
	}
	tokens, err := readCredentialsFile()
	if err != nil {
		return removed, err
	}
	if _, ok := tokens[apiURL]; ok {
		removed = true
	}
	return removed, removeFromFile(apiURL)
}

func removeFromFile(apiURL string) error {
	tokens, err := readCredentialsFile()
	if err != nil {
		return err
	}
	if _, ok := tokens[apiURL]; !ok {
		return nil
	}
	delete(tokens, apiURL)
	return writeCredentialsFile(tokens)
}

func credentialsPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "credentials.json"), nil
}

func readCredentialsFile() (map[string]string, error) {
	tokens := map[string]string{}
	path, err := credentialsPath()
	if err != nil {
		return tokens, err
	}
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return tokens, nil
	}
	if err != nil {
		return tokens, err
	}
	// Tokens readable by others are as good as leaked: refuse rather than
	// use them silently.
	if !isWindows() && info.Mode().Perm()&0o077 != 0 {
		return tokens, fmt.Errorf("%s can be read by other users (mode %o). Run: chmod 600 %s", path, info.Mode().Perm(), path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return tokens, err
	}
	if err := json.Unmarshal(raw, &tokens); err != nil {
		return map[string]string{}, fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	return tokens, nil
}

func writeCredentialsFile(tokens map[string]string) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(tokens, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(dir, "credentials.json", append(raw, '\n'))
}

func isWindows() bool { return runtime.GOOS == "windows" }
