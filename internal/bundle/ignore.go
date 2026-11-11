package bundle

import (
	"bytes"
	"strings"
)

// Pattern is one line of an ignore file, parsed as git's dir.c does
// (parse_path_pattern).
type Pattern struct {
	// Source and Line say where the pattern was read ("" for a default).
	Source string
	Line   int
	// Text is the line as written, after trailing spaces are trimmed.
	Text string

	pattern   string // without "!" and without one trailing "/"
	base      string // the folder it applies under: "" or "sub/dir/"
	negative  bool   // "!": re-include
	mustBeDir bool   // a trailing "/": folders only
	noDir     bool   // no "/" in it: matches a base name at any depth

	// onlyBeside, when set, limits the pattern to entries whose folder
	// also holds a file of this name (the default vendor/ rule).
	onlyBeside string
}

// Negative reports whether the pattern re-includes ("!").
func (p Pattern) Negative() bool { return p.negative }

// ParseIgnore reads an ignore file's content. base is the folder the file
// sits in, relative to the bundle root with forward slashes ("" for the
// root, "sub/dir" otherwise); its patterns apply only under that folder.
//
// The syntax is git's: a "#" at the start of a line is a comment, "\#"
// and "\!" are literal, trailing spaces are trimmed unless escaped with a
// backslash, "!" re-includes, a trailing "/" matches folders only, and a
// "/" at the start or in the middle anchors the pattern to base.
func ParseIgnore(data []byte, base, source string) []Pattern {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")) // a UTF-8 byte order mark
	if base != "" && !strings.HasSuffix(base, "/") {
		base += "/"
	}
	var out []Pattern
	for n, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" || line[0] == '#' {
			continue
		}
		line = trimTrailingSpaces(line)
		if line == "" {
			continue
		}
		p := parsePattern(line)
		p.Source, p.Line, p.base = source, n+1, base
		out = append(out, p)
	}
	return out
}

// trimTrailingSpaces is git's trim_trailing_spaces: unescaped trailing
// spaces go (tabs stay); "\ " keeps the space.
func trimTrailingSpaces(s string) string {
	lastSpace := -1
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ':
			if lastSpace < 0 {
				lastSpace = i
			}
		case '\\':
			i++
			if i >= len(s) {
				return s
			}
			lastSpace = -1
		default:
			lastSpace = -1
		}
	}
	if lastSpace >= 0 {
		return s[:lastSpace]
	}
	return s
}

func parsePattern(line string) Pattern {
	p := Pattern{Text: line}
	s := line
	if s[0] == '!' {
		p.negative = true
		s = s[1:]
	}
	length := len(s)
	if length > 0 && s[length-1] == '/' {
		length--
		p.mustBeDir = true
	}
	p.noDir = strings.IndexByte(s[:length], '/') < 0
	p.pattern = s[:length]
	return p
}

// matches applies one pattern to a path relative to the bundle root, as
// git's last_matching_pattern_from_list does for a single entry.
func (p *Pattern) matches(rel string, isDir bool) bool {
	if p.mustBeDir && !isDir {
		return false
	}
	if p.pattern == "" {
		return false
	}
	if !strings.HasPrefix(rel, p.base) || len(rel) == len(p.base) {
		return false
	}
	name := rel[len(p.base):]
	if p.noDir {
		if i := strings.LastIndexByte(name, '/'); i >= 0 {
			name = name[i+1:]
		}
		return wildmatch(p.pattern, name, 0)
	}
	return matchPathname(name, p.pattern)
}

// matchPathname is git's match_pathname with the base already removed:
// the pattern, less a leading "/", matched against the rest of the path
// with WM_PATHNAME. (git first compares the literal prefix, which is only
// a shortcut: git check-ignore agrees with the whole-pattern match.)
func matchPathname(name, pattern string) bool {
	return wildmatch(strings.TrimPrefix(pattern, "/"), name, wmPathname)
}

// Layer is where a pattern list comes from, lowest precedence first.
type Layer int

const (
	LayerDefault     Layer = iota // the CLI's built-in exclusions
	LayerInfoExclude              // .git/info/exclude
	LayerGitignore                // .gitignore files
	LayerTalksasa                 // .talksasaignore files
)

// Matcher decides whether a path is excluded from a bundle.
//
// Precedence, highest first: .talksasaignore files (the deepest folder's
// first), .gitignore files (the deepest folder's first), .git/info/exclude,
// then the defaults. Within one file the last matching line wins. The first
// list with a matching line decides, so a later layer can re-include ("!")
// what an earlier one excluded, except under an excluded folder: Excluded
// checks every parent folder first, as git does.
type Matcher struct {
	defaults []Pattern
	info     []Pattern
	byLayer  map[Layer]map[string][]Pattern // layer -> folder ("" or "a/b/") -> patterns

	// Exists reports whether a path relative to the bundle root exists;
	// it is asked only for a conditional default (vendor/ beside
	// composer.json). Nil means nothing exists.
	Exists func(rel string) bool
}

// NewMatcher is a matcher with no patterns.
func NewMatcher() *Matcher {
	return &Matcher{byLayer: map[Layer]map[string][]Pattern{
		LayerGitignore: {},
		LayerTalksasa:  {},
	}}
}

// Add appends patterns to a layer. Each pattern applies under its own base.
func (m *Matcher) Add(layer Layer, patterns []Pattern) {
	for _, p := range patterns {
		switch layer {
		case LayerDefault:
			m.defaults = append(m.defaults, p)
		case LayerInfoExclude:
			m.info = append(m.info, p)
		default:
			m.byLayer[layer][p.base] = append(m.byLayer[layer][p.base], p)
		}
	}
}

// Match decides one entry by its own name alone, assuming every parent
// folder is included. It returns the deciding pattern, or nil when none
// matches (the entry is included).
func (m *Matcher) Match(rel string, isDir bool) *Pattern {
	folders := parentFolders(rel)
	for _, layer := range []Layer{LayerTalksasa, LayerGitignore} {
		for _, folder := range folders {
			if p := lastMatch(m.byLayer[layer][folder], rel, isDir, nil); p != nil {
				return p
			}
		}
	}
	if p := lastMatch(m.info, rel, isDir, nil); p != nil {
		return p
	}
	return lastMatch(m.defaults, rel, isDir, m.Exists)
}

// Excluded reports whether rel is left out: it, or a folder above it, is
// matched by a pattern that is not a re-include.
func (m *Matcher) Excluded(rel string, isDir bool) bool {
	parts := strings.Split(rel, "/")
	for i := 1; i < len(parts); i++ {
		if p := m.Match(strings.Join(parts[:i], "/"), true); p != nil && !p.negative {
			return true
		}
	}
	p := m.Match(rel, isDir)
	return p != nil && !p.negative
}

// parentFolders lists the folders above rel, deepest first, each with a
// trailing slash; the root is "".
func parentFolders(rel string) []string {
	out := make([]string, 0, strings.Count(rel, "/")+1)
	for i := len(rel) - 1; i >= 0; i-- {
		if rel[i] == '/' {
			out = append(out, rel[:i+1])
		}
	}
	return append(out, "")
}

func lastMatch(list []Pattern, rel string, isDir bool, exists func(string) bool) *Pattern {
	for i := len(list) - 1; i >= 0; i-- {
		p := &list[i]
		if !p.matches(rel, isDir) {
			continue
		}
		if p.onlyBeside != "" {
			folder := ""
			if j := strings.LastIndexByte(rel, '/'); j >= 0 {
				folder = rel[:j+1]
			}
			if exists == nil || !exists(folder+p.onlyBeside) {
				continue
			}
		}
		return p
	}
	return nil
}

// defaultIgnore is what a bundle leaves out before any ignore file is read.
// An ignore file can re-include any of it with "!".
const defaultIgnore = `.git/
.hg/
.svn/
node_modules/
__pycache__/
.venv/
venv/
.DS_Store
Thumbs.db
.talksasa.json
.env
.env.*
!.env.example
!.env.sample
!.env.template
!.env.dist
`

// DefaultPatterns are the built-in exclusions, including vendor/ only in a
// folder that holds a composer.json (a Go module's vendor/ is kept).
func DefaultPatterns() []Pattern {
	out := ParseIgnore([]byte(defaultIgnore), "", "")
	vendor := parsePattern("vendor/")
	vendor.onlyBeside = "composer.json"
	return append(out, vendor)
}

// exampleEnvNames are .env files meant to be committed.
var exampleEnvNames = map[string]bool{
	".env.example":  true,
	".env.sample":   true,
	".env.template": true,
	".env.dist":     true,
}

// isSecretEnvName reports a base name that usually holds secrets.
func isSecretEnvName(base string) bool {
	return (base == ".env" || strings.HasPrefix(base, ".env.")) && !exampleEnvNames[base]
}
