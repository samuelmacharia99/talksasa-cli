// Package link ties a project folder to one app through .talksasa.json.
//
// The file holds the app's id and name, never a token or an API address:
// it is meant to be committed, and an address in it would let a repository
// send a CI job's TALKSASA_TOKEN wherever it liked.
package link

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// FileName is the link file looked for in a folder and its parents.
const FileName = ".talksasa.json"

// Link is the file's content.
type Link struct {
	ServiceID int    `json:"service_id"`
	Name      string `json:"name,omitempty"`
}

// Find looks for the link file from dir up to the filesystem root, and
// returns it with the path it was read from.
func Find(dir string) (*Link, string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, "", err
	}
	for {
		path := filepath.Join(dir, FileName)
		raw, err := os.ReadFile(path)
		if err == nil {
			var l Link
			if err := json.Unmarshal(raw, &l); err != nil {
				return nil, path, fmt.Errorf("%s is not valid JSON: %w", path, err)
			}
			if l.ServiceID <= 0 {
				return nil, path, fmt.Errorf("%s has no service_id; run talksasa link again", path)
			}
			return &l, path, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, path, err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, "", nil
		}
		dir = parent
	}
}

// Write puts the link file in dir.
func Write(dir string, l Link) (string, error) {
	raw, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, FileName)
	return path, os.WriteFile(path, append(raw, '\n'), 0o644)
}

// Remove deletes the link file in dir, reporting whether there was one.
func Remove(dir string) (bool, error) {
	err := os.Remove(filepath.Join(dir, FileName))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
