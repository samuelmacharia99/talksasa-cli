// Package bundle packs a project folder into the .tar.gz the Talksasa
// platform deploys: the files git would track (and .talksasaignore allows),
// in a byte-for-byte reproducible archive, within the platform's limits.
//
// The same folder always gives the same bytes, so the same sha256: that is
// what lets an interrupted upload resume when the command runs again.
package bundle

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Limits are the platform's caps on a bundle. Zero fields take the default.
type Limits struct {
	MaxFiles      int   // files
	MaxFolders    int   // folders, counting those a path implies
	MaxDepth      int   // path components: a/b/c is 3
	MaxUnpacked   int64 // bytes of file content
	MaxCompressed int64 // bytes of the .tar.gz
}

// DefaultLimits are the platform's (config/containers.php code_upload, and
// BundleInspector's nesting cap).
var DefaultLimits = Limits{
	MaxFiles:      50000,
	MaxFolders:    50000,
	MaxDepth:      64,
	MaxUnpacked:   1024 << 20,
	MaxCompressed: 200 << 20,
}

func (l Limits) withDefaults() Limits {
	if l.MaxFiles <= 0 {
		l.MaxFiles = DefaultLimits.MaxFiles
	}
	if l.MaxFolders <= 0 {
		l.MaxFolders = DefaultLimits.MaxFolders
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = DefaultLimits.MaxDepth
	}
	if l.MaxUnpacked <= 0 {
		l.MaxUnpacked = DefaultLimits.MaxUnpacked
	}
	if l.MaxCompressed <= 0 {
		l.MaxCompressed = DefaultLimits.MaxCompressed
	}
	return l
}

// Options configure Pack.
type Options struct {
	// TempDir is where the archive is written ("" for the system's).
	TempDir string
	Limits  Limits
}

// File is one file in the bundle.
type File struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// TopLevel is one entry at the bundle's root, with what is under it.
type TopLevel struct {
	Name    string `json:"name"`
	Folder  bool   `json:"folder"`
	Bytes   int64  `json:"bytes"`
	Files   int    `json:"files"`
	Folders int    `json:"folders"` // under it, not counting itself
}

// Summary describes a packed bundle.
type Summary struct {
	// Path is the archive on disk. The caller removes it (Remove).
	Path          string `json:"-"`
	SHA256        string `json:"sha256"`
	Size          int64  `json:"compressed_bytes"`
	UnpackedBytes int64  `json:"unpacked_bytes"`
	FileCount     int    `json:"file_count"`
	FolderCount   int    `json:"folder_count"`
	// SkippedLinks counts symbolic links, which are never followed or
	// packed; SkippedOther counts devices, sockets and pipes.
	SkippedLinks int      `json:"skipped_links"`
	SkippedOther int      `json:"skipped_other"`
	LinkExamples []string `json:"skipped_link_examples,omitempty"`
	// EnvFiles are included files named .env or .env.* (other than
	// .env.example and the like), which usually hold secrets.
	EnvFiles []string `json:"env_files"`
	// RootMarkers are the files at the root that say what the app is
	// (package.json, composer.json, ...). Empty is worth a warning.
	RootMarkers []string   `json:"root_markers"`
	TopLevel    []TopLevel `json:"top_level"`
	Files       []File     `json:"-"`
}

// Remove deletes the archive. It is safe to call more than once.
func (s *Summary) Remove() {
	if s != nil && s.Path != "" {
		_ = os.Remove(s.Path)
	}
}

// RootMarkerNames are files whose presence at the root says what kind of
// app the folder is.
var RootMarkerNames = []string{
	"package.json", "composer.json", "artisan", "index.html", "index.php",
	"requirements.txt", "pyproject.toml", "go.mod", "Gemfile", "Procfile",
}

// The fixed metadata every entry carries, so the archive depends only on
// names, content and the executable bit.
var fixedModTime = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

const (
	modeDir  = 0o755
	modeExec = 0o755
	modeFile = 0o644

	// The platform's per-name and per-path limits (BundleInspector).
	maxComponentBytes = 255
	maxPathBytes      = 4096

	// After a limit is passed the walk keeps counting, to name what is
	// large, for at most this many more entries.
	overflowScan = 1_000_000

	ignoreFileGit      = ".gitignore"
	ignoreFileTalksasa = ".talksasaignore"
)

// InputError is a folder the platform would refuse, said plainly.
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

type entry struct {
	rel   string // forward slashes; folders without a trailing slash
	abs   string
	isDir bool
	info  fs.FileInfo // from the walk, to notice a file swapped before it is read
}

type walkState struct {
	ctx     context.Context
	root    string
	limits  Limits
	matcher *Matcher
	sum     *Summary
	entries []entry
	top     map[string]*TopLevel

	over      *LimitError // the first limit passed
	scanned   int         // entries counted since then
	truncated bool        // the overflow scan stopped early
}

var errScanStop = errors.New("stop scanning")

// Pack walks dir and writes the bundle to a temporary file. On error no
// file is left behind. ctx stops it (Ctrl-C).
func Pack(ctx context.Context, dir string, opts Options) (*Summary, error) {
	limits := opts.Limits.withDefaults()

	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	st, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, &InputError{Msg: dir + " is not a folder"}
	}

	w := &walkState{
		ctx:     ctx,
		root:    root,
		limits:  limits,
		matcher: NewMatcher(),
		sum:     &Summary{EnvFiles: []string{}, RootMarkers: []string{}, Files: []File{}},
		top:     map[string]*TopLevel{},
	}
	w.matcher.Exists = func(rel string) bool {
		_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
		return err == nil
	}
	w.matcher.Add(LayerDefault, DefaultPatterns())
	if err := w.loadInfoExclude(); err != nil {
		return nil, err
	}

	err = filepath.WalkDir(root, w.visit)
	if err != nil && !errors.Is(err, errScanStop) {
		return nil, err
	}
	if w.over != nil {
		w.over.AtLeast = w.truncated
		w.over.Top = w.topLevel()
		return nil, w.over
	}
	if w.sum.FileCount == 0 {
		return nil, &InputError{Msg: "there is nothing to deploy in " + dir + ": it has no files, or every file is excluded (see .gitignore and .talksasaignore)"}
	}
	w.sum.TopLevel = w.topLevel()
	sort.Strings(w.sum.RootMarkers)

	if err := w.write(opts.TempDir); err != nil {
		return nil, err
	}
	return w.sum, nil
}

// loadInfoExclude reads .git/info/exclude when the folder is a Git
// checkout's root. A .git file (a worktree or submodule) is not followed.
func (w *walkState) loadInfoExclude() error {
	gitDir := filepath.Join(w.root, ".git")
	st, err := os.Lstat(gitDir)
	if err != nil || !st.IsDir() {
		return nil
	}
	data, ok, err := readIgnoreFile(filepath.Join(gitDir, "info", "exclude"))
	if err != nil {
		return err
	}
	if ok {
		w.matcher.Add(LayerInfoExclude, ParseIgnore(data, "", ".git/info/exclude"))
	}
	return nil
}

// readIgnoreFile reads a regular ignore file. Like git, a symbolic link
// in its place is not followed.
func readIgnoreFile(path string) ([]byte, bool, error) {
	st, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !st.Mode().IsRegular() {
		return nil, false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	return data, true, nil
}

// loadIgnoreFiles reads a folder's .gitignore and .talksasaignore.
func (w *walkState) loadIgnoreFiles(abs, rel string) error {
	for _, f := range []struct {
		name  string
		layer Layer
	}{{ignoreFileGit, LayerGitignore}, {ignoreFileTalksasa, LayerTalksasa}} {
		data, ok, err := readIgnoreFile(filepath.Join(abs, f.name))
		if err != nil {
			return err
		}
		if ok {
			source := f.name
			if rel != "" {
				source = rel + "/" + f.name
			}
			w.matcher.Add(f.layer, ParseIgnore(data, rel, source))
		}
	}
	return nil
}

func (w *walkState) visit(abs string, d fs.DirEntry, walkErr error) error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	if abs == w.root {
		if walkErr != nil {
			return walkErr
		}
		return w.loadIgnoreFiles(abs, "")
	}
	relOS, err := filepath.Rel(w.root, abs)
	if err != nil {
		return err
	}
	rel := filepath.ToSlash(relOS)
	isDir := d.IsDir()

	// A folder's parents were included, or the walk would not be here.
	if p := w.matcher.Match(rel, isDir); p != nil && !p.negative {
		if isDir {
			return filepath.SkipDir
		}
		return nil
	}
	if walkErr != nil {
		return unreadable(rel, walkErr)
	}

	switch typ := d.Type(); {
	case typ&fs.ModeSymlink != 0:
		w.sum.SkippedLinks++
		if len(w.sum.LinkExamples) < 5 {
			w.sum.LinkExamples = append(w.sum.LinkExamples, rel)
		}
		return nil
	case !isDir && !typ.IsRegular():
		w.sum.SkippedOther++
		return nil
	}

	if err := checkName(rel); err != nil {
		return err
	}

	var size int64
	var info fs.FileInfo
	if !isDir {
		info, err = d.Info()
		if err != nil {
			return fmt.Errorf("cannot read %s: %w", rel, err)
		}
		size = info.Size()
	}
	w.count(rel, isDir, size)

	if isDir {
		if err := w.loadIgnoreFiles(abs, rel); err != nil {
			return unreadable(rel, err)
		}
	}

	if w.over != nil {
		w.scanned++
		if w.scanned > overflowScan {
			w.truncated = true
			return errScanStop
		}
		return nil
	}

	w.entries = append(w.entries, entry{rel: rel, abs: abs, isDir: isDir, info: info})
	if !isDir {
		w.sum.Files = append(w.sum.Files, File{Path: rel, Size: size})
		base := rel[strings.LastIndexByte(rel, '/')+1:]
		if isSecretEnvName(base) {
			w.sum.EnvFiles = append(w.sum.EnvFiles, rel)
		}
		if !strings.Contains(rel, "/") {
			for _, marker := range RootMarkerNames {
				if rel == marker {
					w.sum.RootMarkers = append(w.sum.RootMarkers, rel)
				}
			}
		}
	}
	return nil
}

// count adds an entry to the totals and notes the first limit it passes.
func (w *walkState) count(rel string, isDir bool, size int64) {
	first, _, nested := strings.Cut(rel, "/")
	t := w.top[first]
	if t == nil {
		t = &TopLevel{Name: first, Folder: nested || isDir}
		w.top[first] = t
	}
	if isDir {
		w.sum.FolderCount++
		if nested {
			t.Folders++
		}
	} else {
		w.sum.FileCount++
		w.sum.UnpackedBytes += size
		t.Files++
		t.Bytes += size
	}

	if w.over != nil {
		return
	}
	l := w.limits
	switch depth := strings.Count(rel, "/") + 1; {
	case depth > l.MaxDepth:
		w.over = &LimitError{Kind: LimitDepth, Max: int64(l.MaxDepth), Path: rel}
	case w.sum.FileCount > l.MaxFiles:
		w.over = &LimitError{Kind: LimitFiles, Max: int64(l.MaxFiles)}
	case w.sum.FolderCount > l.MaxFolders:
		w.over = &LimitError{Kind: LimitFolders, Max: int64(l.MaxFolders)}
	case w.sum.UnpackedBytes > l.MaxUnpacked:
		w.over = &LimitError{Kind: LimitUnpacked, Max: l.MaxUnpacked}
	}
}

// topLevel lists the root's entries by name, with final totals.
func (w *walkState) topLevel() []TopLevel {
	out := make([]TopLevel, 0, len(w.top))
	for _, t := range w.top {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if w.over != nil {
		w.over.Files, w.over.Folders, w.over.Unpacked = w.sum.FileCount, w.sum.FolderCount, w.sum.UnpackedBytes
	}
	return out
}

// unreadable explains an entry the walk cannot read.
func unreadable(rel string, err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		err = pathErr.Err
	}
	return fmt.Errorf("cannot read %s: %w. Leave it out with a line in .talksasaignore: /%s", rel, err, rel)
}

// checkName refuses a name the platform would refuse, before uploading.
func checkName(rel string) error {
	if len(rel) > maxPathBytes {
		return &InputError{Msg: fmt.Sprintf("%s: the path is longer than the platform's %d bytes. Leave it out with a line in .talksasaignore.", shorten(rel), maxPathBytes)}
	}
	for _, part := range strings.Split(rel, "/") {
		if len(part) > maxComponentBytes {
			return &InputError{Msg: fmt.Sprintf("%s: a name is longer than the platform's %d bytes. Leave it out with a line in .talksasaignore.", shorten(rel), maxComponentBytes)}
		}
	}
	for i := 0; i < len(rel); i++ {
		if c := rel[i]; c < 0x20 || c == 0x7f || c == '\\' {
			return &InputError{Msg: fmt.Sprintf("%q: the name has a control character or a backslash, which the platform refuses. Rename it, or leave it out with a line in .talksasaignore.", rel)}
		}
	}
	return nil
}

func shorten(s string) string {
	if len(s) > 80 {
		return s[:40] + "…" + s[len(s)-30:]
	}
	return s
}

// write packs the chosen entries into a temporary .tar.gz, hashing it as
// it goes.
func (w *walkState) write(tempDir string) (err error) {
	f, err := os.CreateTemp(tempDir, "talksasa-bundle-*.tar.gz")
	if err != nil {
		return fmt.Errorf("create the bundle file: %w", err)
	}
	path := f.Name()
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(path)
		}
	}()

	hash := sha256.New()
	counted := &countingWriter{}
	gz, err := gzip.NewWriterLevel(io.MultiWriter(f, hash, counted), gzip.DefaultCompression)
	if err != nil {
		return err
	}
	// The gzip header's name and time stay empty, so they never vary.
	tw := tar.NewWriter(gz)

	var unpacked int64
	for i := range w.entries {
		if err := w.ctx.Err(); err != nil {
			return err
		}
		e := &w.entries[i]
		n, err := w.writeEntry(tw, e)
		if err != nil {
			return err
		}
		unpacked += n
		if unpacked > w.limits.MaxUnpacked {
			return w.limitAfterWrite(LimitUnpacked, w.limits.MaxUnpacked, unpacked)
		}
		if counted.n > w.limits.MaxCompressed {
			return w.limitAfterWrite(LimitCompressed, w.limits.MaxCompressed, counted.n)
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if counted.n > w.limits.MaxCompressed {
		return w.limitAfterWrite(LimitCompressed, w.limits.MaxCompressed, counted.n)
	}
	if err := f.Close(); err != nil {
		return err
	}

	w.sum.Path = path
	w.sum.SHA256 = hex.EncodeToString(hash.Sum(nil))
	w.sum.Size = counted.n
	w.sum.UnpackedBytes = unpacked
	return nil
}

func (w *walkState) limitAfterWrite(kind LimitKind, max, actual int64) error {
	return &LimitError{
		Kind: kind, Max: max, Actual: actual, AtLeast: true,
		Files: w.sum.FileCount, Folders: w.sum.FolderCount, Unpacked: w.sum.UnpackedBytes,
		Top: w.sum.TopLevel,
	}
}

// writeEntry writes one header and, for a file, its content. It returns
// the bytes of content written.
func (w *walkState) writeEntry(tw *tar.Writer, e *entry) (int64, error) {
	hdr := &tar.Header{
		Format:  tar.FormatPAX,
		ModTime: fixedModTime,
	}
	if e.isDir {
		hdr.Typeflag = tar.TypeDir
		hdr.Name = e.rel + "/"
		hdr.Mode = modeDir
		return 0, tw.WriteHeader(hdr)
	}

	f, err := os.Open(e.abs)
	if err != nil {
		return 0, fmt.Errorf("cannot read %s: %w", e.rel, err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, fmt.Errorf("cannot read %s: %w", e.rel, err)
	}
	// What was opened must be the regular file the walk saw, not a link
	// or anything else put in its place since.
	if !st.Mode().IsRegular() || !os.SameFile(st, e.info) {
		return 0, fmt.Errorf("%s changed while it was being packed; run the command again", e.rel)
	}

	hdr.Typeflag = tar.TypeReg
	hdr.Name = e.rel
	hdr.Size = st.Size()
	hdr.Mode = modeFile
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o111 != 0 {
		hdr.Mode = modeExec
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return 0, err
	}
	n, err := io.CopyN(tw, &contextReader{ctx: w.ctx, r: f}, hdr.Size)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return n, fmt.Errorf("%s changed while it was being packed; run the command again", e.rel)
		}
		if w.ctx.Err() != nil {
			return n, w.ctx.Err()
		}
		return n, fmt.Errorf("cannot read %s: %w", e.rel, err)
	}
	return n, nil
}

type countingWriter struct{ n int64 }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

// contextReader stops a long copy when ctx ends.
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *contextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
