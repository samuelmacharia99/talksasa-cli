package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// writeTree makes files (and, for names ending in "/", folders) under root.
func writeTree(t *testing.T, root string, tree map[string]string) {
	t.Helper()
	for name, content := range tree {
		full := filepath.Join(root, filepath.FromSlash(name))
		if strings.HasSuffix(name, "/") {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func pack(t *testing.T, root string, limits Limits) (*Summary, string) {
	t.Helper()
	tmp := t.TempDir()
	sum, err := Pack(context.Background(), root, Options{TempDir: tmp, Limits: limits})
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	t.Cleanup(sum.Remove)
	return sum, tmp
}

type tarEntry struct {
	hdr  *tar.Header
	body string
}

func readBundle(t *testing.T, path string) (*gzip.Header, []tarEntry) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	header := gz.Header
	tr := tar.NewReader(gz)
	var out []tarEntry
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		out = append(out, tarEntry{hdr: hdr, body: string(body)})
	}
	return &header, out
}

func names(entries []tarEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.hdr.Name
	}
	return out
}

func TestPackChoosesFilesLikeGitWithTheDefaults(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"composer.json":           "{}",
		"index.php":               "<?php",
		".env":                    "SECRET=1",
		".env.example":            "SECRET=",
		".env.production":         "SECRET=2",
		"config/.env.staging":     "SECRET=3",
		".talksasa.json":          `{"service_id":7}`,
		".git/config":             "[core]",
		".git/info/exclude":       "private.txt\n",
		"private.txt":             "p",
		"node_modules/x/index.js": "x",
		"vendor/autoload.php":     "<?php",
		"tools/vendor/mod/a.go":   "package mod",
		"tools/go.mod":            "module tools",
		"app/__pycache__/a.pyc":   "x",
		".DS_Store":               "x",
		".gitignore":              "dist/\n*.log\n",
		"dist/app.js":             "built",
		"debug.log":               "log",
		"keep.log":                "kept",
		".talksasaignore":         "!keep.log\ndocs/\n!.env.production\n",
		"docs/readme.md":          "docs",
		"src/main.php":            "<?php",
		"src/.gitignore":          "local/\n",
		"src/local/x.php":         "x",
		"public/empty/":           "",
	})

	sum, _ := pack(t, root, Limits{})
	_, entries := readBundle(t, sum.Path)

	want := []string{
		".env.example", ".env.production", ".gitignore", ".talksasaignore",
		"app/", "composer.json", "config/",
		"index.php", "keep.log", "public/", "public/empty/",
		"src/", "src/.gitignore", "src/main.php",
		"tools/", "tools/go.mod", "tools/vendor/", "tools/vendor/mod/", "tools/vendor/mod/a.go",
	}
	if got := names(entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("bundle holds\n  %v\nwant\n  %v", got, want)
	}
	if !reflect.DeepEqual(sum.EnvFiles, []string{".env.production"}) {
		t.Fatalf("env warnings %v", sum.EnvFiles)
	}
	if !reflect.DeepEqual(sum.RootMarkers, []string{"composer.json", "index.php"}) {
		t.Fatalf("root markers %v", sum.RootMarkers)
	}
	if sum.FileCount != 11 || sum.FolderCount != 8 {
		t.Fatalf("counted %d files, %d folders", sum.FileCount, sum.FolderCount)
	}
	if len(sum.Files) != sum.FileCount {
		t.Fatalf("file list has %d entries", len(sum.Files))
	}
}

func TestPackIsDeterministic(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"package.json":    `{"name":"x"}`,
		"src/index.js":    strings.Repeat("console.log(1)\n", 500),
		"src/lib/util.js": "export {}",
		"assets/logo.svg": "<svg/>",
		"a-very-long-folder-name-" + strings.Repeat("x", 90) + "/file.txt": "long names go in PAX records",
	})

	first, _ := pack(t, root, Limits{})
	second, _ := pack(t, root, Limits{})
	a, _ := os.ReadFile(first.Path)
	b, _ := os.ReadFile(second.Path)
	if !bytes.Equal(a, b) || first.SHA256 != second.SHA256 {
		t.Fatal("packing the same folder twice gave different bytes")
	}
	sum := sha256.Sum256(a)
	if hex.EncodeToString(sum[:]) != first.SHA256 || int64(len(a)) != first.Size {
		t.Fatal("the summary's sha256 or size does not describe the file")
	}

	// Times and ownership on disk do not reach the archive.
	old := time.Date(2011, 3, 4, 5, 6, 7, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(root, "src", "index.js"), old, old); err != nil {
		t.Fatal(err)
	}
	third, _ := pack(t, root, Limits{})
	if third.SHA256 != first.SHA256 {
		t.Fatal("a file's modification time changed the bundle")
	}

	gzHeader, entries := readBundle(t, first.Path)
	if gzHeader.Name != "" || !gzHeader.ModTime.IsZero() {
		t.Fatalf("gzip header carries name %q, time %v", gzHeader.Name, gzHeader.ModTime)
	}
	for _, e := range entries {
		h := e.hdr
		if strings.HasPrefix(h.Name, "./") || strings.HasPrefix(h.Name, "/") || strings.Contains(h.Name, `\`) {
			t.Errorf("entry name %q", h.Name)
		}
		if h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" {
			t.Errorf("%s: owner %d/%d %q/%q", h.Name, h.Uid, h.Gid, h.Uname, h.Gname)
		}
		if !h.ModTime.Equal(fixedModTime) {
			t.Errorf("%s: time %v", h.Name, h.ModTime)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if !strings.HasSuffix(h.Name, "/") || h.Mode != 0o755 {
				t.Errorf("folder %s mode %o", h.Name, h.Mode)
			}
		case tar.TypeReg:
			if h.Mode != 0o644 {
				t.Errorf("file %s mode %o", h.Name, h.Mode)
			}
		default:
			t.Errorf("%s: type %c", h.Name, h.Typeflag)
		}
	}
}

func TestPackKeepsTheExecutableBit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no executable bit; files are always 0644")
	}
	root := t.TempDir()
	writeTree(t, root, map[string]string{"run.sh": "#!/bin/sh", "data.txt": "x"})
	if err := os.Chmod(filepath.Join(root, "run.sh"), 0o700); err != nil {
		t.Fatal(err)
	}
	sum, _ := pack(t, root, Limits{})
	_, entries := readBundle(t, sum.Path)
	modes := map[string]int64{}
	for _, e := range entries {
		modes[e.hdr.Name] = e.hdr.Mode
	}
	if modes["run.sh"] != 0o755 || modes["data.txt"] != 0o644 {
		t.Fatalf("modes %v", modes)
	}
}

func TestPackUsesForwardSlashesAndRefusesBackslashNames(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a/b/c/d.txt": "x"})
	sum, _ := pack(t, root, Limits{})
	_, entries := readBundle(t, sum.Path)
	if got := names(entries); !reflect.DeepEqual(got, []string{"a/", "a/b/", "a/b/c/", "a/b/c/d.txt"}) {
		t.Fatalf("names %v", got)
	}
	if sum.Files[0].Path != "a/b/c/d.txt" {
		t.Fatalf("file list %v", sum.Files)
	}

	if runtime.GOOS == "windows" {
		return // a backslash cannot be in a Windows file name
	}
	writeTree(t, root, map[string]string{`a/odd\name.txt`: "x"})
	_, err := Pack(context.Background(), root, Options{TempDir: t.TempDir()})
	var input *InputError
	if !errors.As(err, &input) || !strings.Contains(err.Error(), "backslash") {
		t.Fatalf("want a refusal naming the backslash, got %v", err)
	}
}

func TestPackSkipsLinksAndNeverFollowsThem(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeTree(t, root, map[string]string{"index.html": "<html>", "real/file.txt": "x"})
	writeTree(t, outside, map[string]string{"secret.txt": "do not ship"})
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("cannot make symlinks here: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}
	// An ignored link is simply ignored, not counted.
	if err := os.WriteFile(filepath.Join(root, ".talksasaignore"), []byte("ignored-link\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "ignored-link")); err != nil {
		t.Fatal(err)
	}

	sum, _ := pack(t, root, Limits{})
	_, entries := readBundle(t, sum.Path)
	for _, e := range entries {
		if strings.Contains(e.body, "do not ship") || strings.HasPrefix(e.hdr.Name, "link") {
			t.Fatalf("a link was packed: %s", e.hdr.Name)
		}
	}
	if sum.SkippedLinks != 2 || !reflect.DeepEqual(sum.LinkExamples, []string{"link.txt", "linkdir"}) {
		t.Fatalf("skipped %d links %v", sum.SkippedLinks, sum.LinkExamples)
	}
}

func limitError(t *testing.T, root string, limits Limits) *LimitError {
	t.Helper()
	tmp := t.TempDir()
	_, err := Pack(context.Background(), root, Options{TempDir: tmp, Limits: limits})
	var le *LimitError
	if !errors.As(err, &le) {
		t.Fatalf("want a LimitError, got %v", err)
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Fatalf("a refused pack left %d files behind", len(left))
	}
	return le
}

func TestPackEnforcesThePlatformLimits(t *testing.T) {
	root := t.TempDir()
	tree := map[string]string{"package.json": "{}", "src/a.js": "a", "src/b.js": "b"}
	for i := 0; i < 6; i++ {
		tree["data/file"+string(rune('a'+i))+".bin"] = strings.Repeat("d", 100)
	}
	writeTree(t, root, tree)

	t.Run("files", func(t *testing.T) {
		le := limitError(t, root, Limits{MaxFiles: 5})
		msg := le.Error()
		// The walk keeps counting past the limit, so the totals are real.
		if le.Kind != LimitFiles || le.Files != 9 || le.AtLeast {
			t.Fatalf("%+v", le)
		}
		for _, want := range []string{"9 files", "at most 5", "data/", "6 files", "src/", ".talksasaignore", "/data/"} {
			if !strings.Contains(msg, want) {
				t.Errorf("message lacks %q:\n%s", want, msg)
			}
		}
		if strings.Index(msg, "data/") > strings.Index(msg, "src/") {
			t.Errorf("largest folder not first:\n%s", msg)
		}
	})
	t.Run("unpacked size", func(t *testing.T) {
		le := limitError(t, root, Limits{MaxUnpacked: 300})
		if le.Kind != LimitUnpacked || !strings.Contains(le.Error(), "/data/") {
			t.Fatalf("%v", le)
		}
	})
	t.Run("compressed size", func(t *testing.T) {
		noise := t.TempDir()
		random := make([]byte, 64<<10)
		if _, err := rand.Read(random); err != nil {
			t.Fatal(err)
		}
		writeTree(t, noise, map[string]string{"blob/a.bin": string(random), "index.html": "x"})
		le := limitError(t, noise, Limits{MaxCompressed: 32 << 10})
		if le.Kind != LimitCompressed || !strings.Contains(le.Error(), "compressed") || !strings.Contains(le.Error(), "/blob/") {
			t.Fatalf("%v", le)
		}
	})
}

func TestPackCountsImpliedFoldersAndDepth(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"index.html": "x", "a/b/c/d/e/f.txt": "deep"})

	le := limitError(t, root, Limits{MaxFolders: 4})
	if le.Kind != LimitFolders || le.Folders != 5 || !strings.Contains(le.Error(), "5 folders") {
		t.Fatalf("%v (%+v)", le, le)
	}

	le = limitError(t, root, Limits{MaxDepth: 5})
	if le.Kind != LimitDepth || le.Path != "a/b/c/d/e/f.txt" || !strings.Contains(le.Error(), "6 levels") || !strings.Contains(le.Error(), "/a/") {
		t.Fatalf("%v", le)
	}
	// Exactly at the limit is allowed: f.txt is 6 levels deep.
	pack(t, root, Limits{MaxDepth: 6})
}

func TestPackRefusesAnEmptyResult(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{".env": "x", "node_modules/a.js": "x", "empty/": ""})
	_, err := Pack(context.Background(), root, Options{TempDir: t.TempDir()})
	var input *InputError
	if !errors.As(err, &input) || !strings.Contains(err.Error(), "nothing to deploy") {
		t.Fatalf("got %v", err)
	}
}

func TestPackStopsAndCleansUpWhenCancelled(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"index.html": "x"})
	tmp := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Pack(ctx, root, Options{TempDir: tmp})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Fatal("a cancelled pack left its file behind")
	}
}

func TestPackRefusesWhatIsNotAFolder(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"file.txt": "x"})
	_, err := Pack(context.Background(), filepath.Join(root, "file.txt"), Options{TempDir: t.TempDir()})
	var input *InputError
	if !errors.As(err, &input) {
		t.Fatalf("got %v", err)
	}
}

func TestPackRefusesAnUnreadableFile(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX permissions and a non-root user")
	}
	root := t.TempDir()
	writeTree(t, root, map[string]string{"index.html": "x", "locked/secret": "x"})
	locked := filepath.Join(root, "locked")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	tmp := t.TempDir()
	_, err := Pack(context.Background(), root, Options{TempDir: tmp})
	if err == nil || !strings.Contains(err.Error(), "cannot read locked") || !strings.Contains(err.Error(), ".talksasaignore") {
		t.Fatalf("got %v", err)
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Fatal("a failed pack left its file behind")
	}
}

func TestThousandsAndHumanBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 50000: "50,000", 1234567: "1,234,567"} {
		if got := thousands(n); got != want {
			t.Errorf("thousands(%d) = %q", n, got)
		}
	}
	if got := HumanBytes(200 << 20); got != "200.0 MiB" {
		t.Errorf("HumanBytes = %q", got)
	}
}
