package bundle

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Wildmatch cases from git's t/t3070-wildmatch.sh: text, pattern, and
// whether it matches with WM_PATHNAME ("wildmatch") and without
// ("pathmatch"). Case-folding columns are left out: matching here is
// always case-sensitive.
var wildmatchCases = []struct {
	text, pattern        string
	pathname, noPathname bool
}{
	// Basic wildmatch features
	{"foo", "foo", true, true},
	{"foo", "bar", false, false},
	{"", "", true, true},
	{"foo", "???", true, true},
	{"foo", "??", false, false},
	{"foo", "*", true, true},
	{"foo", "f*", true, true},
	{"foo", "*f", false, false},
	{"foo", "*foo*", true, true},
	{"foobar", "*ob*a*r*", true, true},
	{"aaaaaaabababab", "*ab", true, true},
	{"foo*", `foo\*`, true, true},
	{"foobar", `foo\*bar`, false, false},
	{`f\oo`, `f\\oo`, true, true},
	{"ball", "*[al]?", true, true},
	{"ten", "[ten]", false, false},
	{"ten", "**[!te]", true, true},
	{"ten", "**[!ten]", false, false},
	{"ten", "t[a-g]n", true, true},
	{"ten", "t[!a-g]n", false, false},
	{"ton", "t[!a-g]n", true, true},
	{"ton", "t[^a-g]n", true, true},
	{"a]b", "a[]]b", true, true},
	{"a-b", "a[]-]b", true, true},
	{"a]b", "a[]-]b", true, true},
	{"aab", "a[]-]b", false, false},
	{"aab", "a[]a-]b", true, true},
	{"]", "]", true, true},

	// Extended slash-matching features
	{"foo/baz/bar", "foo*bar", false, true},
	{"foo/baz/bar", "foo**bar", false, true},
	{"foobazbar", "foo**bar", true, true},
	{"foo/baz/bar", "foo/**/bar", true, true},
	{"foo/baz/bar", "foo/**/**/bar", true, false},
	{"foo/b/a/z/bar", "foo/**/bar", true, true},
	{"foo/b/a/z/bar", "foo/**/**/bar", true, true},
	{"foo/bar", "foo/**/bar", true, false},
	{"foo/bar", "foo/**/**/bar", true, false},
	{"foo/bar", "foo?bar", false, true},
	{"foo/bar", "foo[/]bar", false, true},
	{"foo/bar", "foo[^a-z]bar", false, true},
	{"foo/bar", "f[^eiu][^eiu][^eiu][^eiu][^eiu]r", false, true},
	{"foo-bar", "f[^eiu][^eiu][^eiu][^eiu][^eiu]r", true, true},
	{"foo", "**/foo", true, false},
	{"XXX/foo", "**/foo", true, true},
	{"bar/baz/foo", "**/foo", true, true},
	{"bar/baz/foo", "*/foo", false, true},
	{"foo/bar/baz", "**/bar*", false, true},
	{"deep/foo/bar/baz", "**/bar/*", true, true},
	{"deep/foo/bar/baz/", "**/bar/*", false, true},
	{"deep/foo/bar/baz/", "**/bar/**", true, true},
	{"deep/foo/bar", "**/bar/*", false, false},
	{"deep/foo/bar/", "**/bar/**", true, true},
	{"foo/bar/baz", "**/bar**", false, true},
	{"foo/bar/baz/x", "*/bar/**", true, true},
	{"deep/foo/bar/baz/x", "*/bar/**", false, true},
	{"deep/foo/bar/baz/x", "**/bar/*/*", true, true},

	// Various additional tests
	{"acrt", "a[c-c]st", false, false},
	{"acrt", "a[c-c]rt", true, true},
	{"]", "[!]-]", false, false},
	{"a", "[!]-]", true, true},
	{"", `\`, false, false},
	{`\`, `\`, false, false},
	{`XXX/\`, `*/\`, false, false},
	{`XXX/\`, `*/\\`, true, true},
	{"foo", "foo", true, true},
	{"@foo", "@foo", true, true},
	{"foo", "@foo", false, false},
	{"[ab]", `\[ab]`, true, true},
	{"[ab]", "[[]ab]", true, true},
	{"[ab]", "[[:]ab]", true, true},
	{"[ab]", "[[::]ab]", false, false},
	{"[ab]", "[[:digit]ab]", true, true},
	{"[ab]", `[\[:]ab]`, true, true},
	{"?a?b", `\??\?b`, true, true},
	{"abc", `\a\b\c`, true, true},
	{"foo", "", false, false},
	{"foo/bar/baz/to", "**/t[o]", true, true},

	// Character class tests
	{"a1B", "[[:alpha:]][[:digit:]][[:upper:]]", true, true},
	{"a", "[[:digit:][:upper:][:space:]]", false, false},
	{"A", "[[:digit:][:upper:][:space:]]", true, true},
	{"1", "[[:digit:][:upper:][:space:]]", true, true},
	{"1", "[[:digit:][:upper:][:spaci:]]", false, false},
	{" ", "[[:digit:][:upper:][:space:]]", true, true},
	{".", "[[:digit:][:upper:][:space:]]", false, false},
	{".", "[[:digit:][:punct:][:space:]]", true, true},
	{"5", "[[:xdigit:]]", true, true},
	{"f", "[[:xdigit:]]", true, true},
	{"D", "[[:xdigit:]]", true, true},
	{"_", "[[:alnum:][:alpha:][:blank:][:cntrl:][:digit:][:graph:][:lower:][:print:][:punct:][:space:][:upper:][:xdigit:]]", true, true},
	{".", "[^[:alnum:][:alpha:][:blank:][:cntrl:][:digit:][:lower:][:space:][:upper:][:xdigit:]]", true, true},
	{"5", "[a-c[:digit:]x-z]", true, true},
	{"b", "[a-c[:digit:]x-z]", true, true},
	{"y", "[a-c[:digit:]x-z]", true, true},
	{"q", "[a-c[:digit:]x-z]", false, false},

	// Additional tests, including some malformed wildmatch patterns
	{"]", `[\\-^]`, true, true},
	{"[", `[\\-^]`, false, false},
	{"-", `[\-_]`, true, true},
	{"]", `[\]]`, true, true},
	{`\]`, `[\]]`, false, false},
	{`\`, `[\]]`, false, false},
	{"ab", "a[]b", false, false},
	{"a[]b", "a[]b", false, false},
	{"ab[", "ab[", false, false},
	{"ab", "[!", false, false},
	{"ab", "[-", false, false},
	{"-", "[-]", true, true},
	{"-", "[a-", false, false},
	{"-", "[!a-", false, false},
	{"-", "[--A]", true, true},
	{"5", "[--A]", true, true},
	{" ", "[ --]", true, true},
	{"$", "[ --]", true, true},
	{"-", "[ --]", true, true},
	{"0", "[ --]", false, false},
	{"-", "[---]", true, true},
	{"-", "[------]", true, true},
	{"j", "[a-e-n]", false, false},
	{"-", "[a-e-n]", true, true},
	{"a", "[!------]", true, true},
	{"[", "[]-a]", false, false},
	{"^", "[]-a]", true, true},
	{"^", "[!]-a]", false, false},
	{"[", "[!]-a]", true, true},
	{"^", `[a^bc]`, true, true},
	{"-b]", "[a-]b]", true, true},
	{`\`, `[\]`, false, false},
	{`\`, `[\\]`, true, true},
	{`\`, `[!\\]`, false, false},
	{"G", `[A-\\]`, true, true},
	{"aaabbb", "b*a", false, false},
	{"aabcaa", "*ba*", false, false},
	{",", "[,]", true, true},
	{",", `[\\,]`, true, true},
	{`\`, `[\\,]`, true, true},
	{"-", "[,-.]", true, true},
	{"+", "[,-.]", false, false},
	{"-.]", "[,-.]", false, false},
	{"2", `[\1-\3]`, true, true},
	{"3", `[\1-\3]`, true, true},
	{"4", `[\1-\3]`, false, false},
	{`\`, `[[-\]]`, true, true},
	{"[", `[[-\]]`, true, true},
	{"]", `[[-\]]`, true, true},
	{"-", `[[-\]]`, false, false},

	// Recursion and the abort code
	{"-adobe-courier-bold-o-normal--12-120-75-75-m-70-iso8859-1", "-*-*-*-*-*-*-12-*-*-*-m-*-*-*", true, true},
	{"-adobe-courier-bold-o-normal--12-120-75-75-X-70-iso8859-1", "-*-*-*-*-*-*-12-*-*-*-m-*-*-*", false, false},
	{"-adobe-courier-bold-o-normal--12-120-75-75-/-70-iso8859-1", "-*-*-*-*-*-*-12-*-*-*-m-*-*-*", false, false},
	{"XXX/adobe/courier/bold/o/normal//12/120/75/75/m/70/iso8859/1", "XXX/*/*/*/*/*/*/12/*/*/*/m/*/*/*", true, true},
	{"XXX/adobe/courier/bold/o/normal//12/120/75/75/X/70/iso8859/1", "XXX/*/*/*/*/*/*/12/*/*/*/m/*/*/*", false, false},
	{"abcd/abcdefg/abcdefghijk/abcdefghijklmnop.txt", "**/*a*b*g*n*t", true, true},
	{"abcd/abcdefg/abcdefghijk/abcdefghijklmnop.txtz", "**/*a*b*g*n*t", false, false},
	{"foo", "*/*/*", false, false},
	{"foo/bar", "*/*/*", false, false},
	{"foo/bba/arr", "*/*/*", true, true},
	{"foo/bb/aa/rr", "*/*/*", false, true},
	{"foo/bb/aa/rr", "**/**/**", true, true},
	{"abcXdefXghi", "*X*i", true, true},
	{"ab/cXd/efXg/hi", "*X*i", false, true},
	{"ab/cXd/efXg/hi", "*/*X*/*/*i", true, true},
	{"ab/cXd/efXg/hi", "**/*X*/**/*i", true, true},

	// Extra pathmatch tests
	{"foo", "fo", false, false},
	{"foo/bar", "foo/bar", true, true},
	{"foo/bar", "foo/*", true, true},
	{"foo/bba/arr", "foo/*", false, true},
	{"foo/bba/arr", "foo/**", true, true},
	{"foo/bba/arr", "foo*", false, true},
	{"foo/bba/arr", "foo**", false, true},
	{"foo/bba/arr", "foo/*arr", false, true},
	{"foo/bba/arr", "foo/**arr", false, true},
	{"foo/bba/arr", "foo/*z", false, false},
	{"foo/bba/arr", "foo/**z", false, false},
	{"foo/bar", "foo?bar", false, true},
	{"foo/bar", "foo[/]bar", false, true},
	{"foo/bar", "foo[^a-z]bar", false, true},
	{"ab/cXd/efXg/hi", "*Xg*i", false, true},
}

func TestWildmatchAgreesWithGitsTable(t *testing.T) {
	for _, c := range wildmatchCases {
		if got := wildmatch(c.pattern, c.text, wmPathname); got != c.pathname {
			t.Errorf("wildmatch(%q, %q, WM_PATHNAME) = %v, want %v", c.pattern, c.text, got, c.pathname)
		}
		if got := wildmatch(c.pattern, c.text, 0); got != c.noPathname {
			t.Errorf("wildmatch(%q, %q, 0) = %v, want %v", c.pattern, c.text, got, c.noPathname)
		}
	}
}

// matcherFor builds a matcher from ignore files keyed by their folder
// ("" for the root), all in the .gitignore layer, with no defaults.
func matcherFor(files map[string]string) *Matcher {
	m := NewMatcher()
	folders := make([]string, 0, len(files))
	for f := range files {
		folders = append(folders, f)
	}
	sort.Strings(folders)
	for _, f := range folders {
		m.Add(LayerGitignore, ParseIgnore([]byte(files[f]), f, f+"/.gitignore"))
	}
	return m
}

type ignoreCase struct {
	path     string
	isDir    bool
	excluded bool
}

func checkIgnore(t *testing.T, m *Matcher, cases []ignoreCase) {
	t.Helper()
	for _, c := range cases {
		if got := m.Excluded(c.path, c.isDir); got != c.excluded {
			t.Errorf("Excluded(%q, dir=%v) = %v, want %v", c.path, c.isDir, got, c.excluded)
		}
	}
}

// The examples in git's gitignore(5).
func TestGitignoreDocumentedExamples(t *testing.T) {
	t.Run("hello.* matches any name starting hello.", func(t *testing.T) {
		checkIgnore(t, matcherFor(map[string]string{"": "hello.*\n"}), []ignoreCase{
			{"hello.c", false, true},
			{"a/b/hello.txt", false, true},
			{"hello", false, false},
		})
	})
	t.Run("doc/frotz/ and /doc/frotz/ are the same, frotz/ matches at any depth", func(t *testing.T) {
		for _, p := range []string{"doc/frotz/", "/doc/frotz/"} {
			checkIgnore(t, matcherFor(map[string]string{"": p}), []ignoreCase{
				{"doc/frotz", true, true},
				{"a/doc/frotz", true, false},
				{"doc/frotz", false, false},
			})
		}
		checkIgnore(t, matcherFor(map[string]string{"": "frotz/"}), []ignoreCase{
			{"frotz", true, true},
			{"a/frotz", true, true},
			{"a/frotz/x", false, true},
			{"frotz", false, false},
		})
	})
	t.Run("foo/* matches foo/test.json and folder foo/bar, not foo/bar/hello.c by itself", func(t *testing.T) {
		m := matcherFor(map[string]string{"": "foo/*\n"})
		checkIgnore(t, m, []ignoreCase{
			{"foo/test.json", false, true},
			{"foo/bar", true, true},
			{"foo", true, false},
		})
		if p := m.Match("foo/bar/hello.c", false); p != nil {
			t.Errorf("foo/* matched foo/bar/hello.c by itself (%q)", p.Text)
		}
	})
	t.Run("**/foo matches foo anywhere; **/foo/bar matches bar under any foo", func(t *testing.T) {
		checkIgnore(t, matcherFor(map[string]string{"": "**/foo\n**/foo/bar\n"}), []ignoreCase{
			{"foo", false, true},
			{"a/b/foo", true, true},
			{"x/foo/bar", false, true},
		})
	})
	t.Run("abc/** matches everything inside abc, not abc", func(t *testing.T) {
		checkIgnore(t, matcherFor(map[string]string{"": "abc/**\n"}), []ignoreCase{
			{"abc/x", false, true},
			{"abc/x/y/z", false, true},
			{"abc", true, false},
			{"a/abc/x", false, false},
		})
	})
	t.Run("a/**/b matches a/b, a/x/b, a/x/y/b", func(t *testing.T) {
		checkIgnore(t, matcherFor(map[string]string{"": "a/**/b\n"}), []ignoreCase{
			{"a/b", false, true},
			{"a/x/b", false, true},
			{"a/x/y/b", false, true},
			{"ab", false, false},
		})
	})
	t.Run("exclude everything except foo/bar", func(t *testing.T) {
		checkIgnore(t, matcherFor(map[string]string{"": "/*\n!/foo\n/foo/*\n!/foo/bar\n"}), []ignoreCase{
			{"foo/bar", true, false},
			{"foo/bar/x", false, false},
			{"foo/baz", true, true},
			{"other", false, true},
			{"foo", true, false},
		})
	})
	t.Run("a file cannot be re-included under an excluded folder", func(t *testing.T) {
		checkIgnore(t, matcherFor(map[string]string{"": "build/\n!build/keep.txt\n"}), []ignoreCase{
			{"build/keep.txt", false, true},
		})
		checkIgnore(t, matcherFor(map[string]string{"": "build/*\n!build/keep.txt\n"}), []ignoreCase{
			{"build/keep.txt", false, false},
			{"build/other.txt", false, true},
		})
	})
}

func TestGitignoreSyntaxDetails(t *testing.T) {
	cases := []struct {
		name     string
		patterns string
		cases    []ignoreCase
	}{
		{"comments and blank lines", "# comment\n\n   \nfoo\n", []ignoreCase{{"# comment", false, false}, {"foo", false, true}}},
		{"escaped hash and bang", "\\#notes\n\\!important\n", []ignoreCase{{"#notes", false, true}, {"!important", false, true}, {"important", false, false}}},
		{"trailing spaces trimmed, escaped one kept", "trail   \nkeep\\ \n", []ignoreCase{{"trail", false, true}, {"trail ", false, false}, {"keep ", false, true}, {"keep", false, false}}},
		{"tab is not trimmed", "tab\t\n", []ignoreCase{{"tab", false, false}, {"tab\t", false, true}}},
		{"CRLF line endings", "one\r\ntwo\r\n", []ignoreCase{{"one", false, true}, {"two", false, true}}},
		{"last match wins", "*.log\n!keep.log\n", []ignoreCase{{"a.log", false, true}, {"keep.log", false, false}}},
		{"order matters", "!keep.log\n*.log\n", []ignoreCase{{"keep.log", false, true}}},
		{"leading slash anchors", "/root.txt\n", []ignoreCase{{"root.txt", false, true}, {"sub/root.txt", false, false}}},
		{"middle slash anchors", "a/b\n", []ignoreCase{{"a/b", false, true}, {"x/a/b", false, false}}},
		{"trailing slash: folders only", "out/\n", []ignoreCase{{"out", true, true}, {"out", false, false}, {"x/out", true, true}}},
		{"leading **", "**/logs\n", []ignoreCase{{"logs", true, true}, {"a/b/logs", false, true}}},
		{"trailing **", "cache/**\n", []ignoreCase{{"cache/a/b", false, true}, {"cache", true, false}}},
		{"middle **", "a/**/z\n", []ignoreCase{{"a/z", false, true}, {"a/b/c/z", false, true}, {"b/a/z", false, false}}},
		{"star does not cross slash when anchored", "/*.txt\n", []ignoreCase{{"a.txt", false, true}, {"d/a.txt", false, false}}},
		{"question mark", "file?.txt\n", []ignoreCase{{"file1.txt", false, true}, {"file10.txt", false, false}}},
		{"class and range", "[a-c]*.go\n", []ignoreCase{{"b.go", false, true}, {"d.go", false, false}}},
		{"negated class with ! and ^", "x[!0-9]\ny[^0-9]\n", []ignoreCase{{"xa", false, true}, {"x1", false, false}, {"yb", false, true}, {"y2", false, false}}},
		{"escaped star", "lit\\*\n", []ignoreCase{{"lit*", false, true}, {"litx", false, false}}},
		{"** not between slashes is one star", "foo**/bar\n", []ignoreCase{{"foo/a/b/bar", false, false}, {"foox/bar", false, true}}},
		{"byte order mark", "\xef\xbb\xbfbom\n", []ignoreCase{{"bom", false, true}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			checkIgnore(t, matcherFor(map[string]string{"": c.patterns}), c.cases)
		})
	}
}

func TestNestedIgnoreFilesApplyToTheirOwnSubtree(t *testing.T) {
	m := matcherFor(map[string]string{
		"":         "*.log\n",
		"sub/":     "!keep.log\n/local.txt\ndeep/\n",
		"sub/deep": "never-read\n",
		"other/":   "x/y\n",
	})
	checkIgnore(t, m, []ignoreCase{
		{"a.log", false, true},
		{"sub/a.log", false, true},
		{"sub/keep.log", false, false}, // the deeper file wins
		{"keep.log", false, true},      // but only under sub/
		{"sub/local.txt", false, true},
		{"local.txt", false, false},
		{"sub/x/local.txt", false, false}, // anchored to sub/
		{"sub/deep", true, true},
		{"other/x/y", false, true},
		{"x/y", false, false},
	})
}

func TestLayerPrecedence(t *testing.T) {
	m := NewMatcher()
	m.Add(LayerDefault, DefaultPatterns())
	m.Add(LayerInfoExclude, ParseIgnore([]byte("secret.txt\n!.env.local\n"), "", ".git/info/exclude"))
	m.Add(LayerGitignore, ParseIgnore([]byte("dist/\n!secret.txt\n"), "", ".gitignore"))
	m.Add(LayerTalksasa, ParseIgnore([]byte("!dist/\n*.md\n"), "", ".talksasaignore"))
	checkIgnore(t, m, []ignoreCase{
		{"dist", true, false},        // .talksasaignore re-includes what .gitignore excluded
		{"secret.txt", false, false}, // .gitignore beats info/exclude
		{".env.local", false, false}, // info/exclude beats the defaults
		{".env", false, true},
		{"README.md", false, true},
		{"node_modules", true, true},
	})
}

func TestDefaults(t *testing.T) {
	m := NewMatcher()
	m.Add(LayerDefault, DefaultPatterns())
	existing := map[string]bool{"composer.json": true, "api/composer.json": true}
	m.Exists = func(rel string) bool { return existing[rel] }
	checkIgnore(t, m, []ignoreCase{
		{".git", true, true},
		{"sub/.hg", true, true},
		{".svn", true, true},
		{"node_modules", true, true},
		{"web/node_modules", true, true},
		{"app/__pycache__", true, true},
		{".venv", true, true},
		{"venv", true, true},
		{".DS_Store", false, true},
		{"img/Thumbs.db", false, true},
		{".talksasa.json", false, true},
		{".env", false, true},
		{"config/.env", false, true},
		{".env.production", false, true},
		{".env.local", false, true},
		{".env.example", false, false},
		{".env.sample", false, false},
		{".env.template", false, false},
		{".env.dist", false, false},
		{"vendor", true, true},       // beside composer.json
		{"api/vendor", true, true},   // beside api/composer.json
		{"tool/vendor", true, false}, // a Go module's vendor/
		{"vendor", false, false},     // a file named vendor
		{"src/main.go", false, false},
		{"venvs", true, false},
	})
}

func TestTalksasaignoreCanReincludeADefault(t *testing.T) {
	m := NewMatcher()
	m.Add(LayerDefault, DefaultPatterns())
	m.Exists = func(string) bool { return true }
	m.Add(LayerTalksasa, ParseIgnore([]byte("!vendor/\n!.env.production\n"), "", ".talksasaignore"))
	checkIgnore(t, m, []ignoreCase{
		{"vendor", true, false},
		{".env.production", false, false},
		{".env", false, true},
	})
}

// TestMatcherAgreesWithGit runs the same ignore files and paths through
// git check-ignore, when git is installed.
func TestMatcherAgreesWithGit(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}

	files := map[string]string{
		"": strings.Join([]string{
			"*.log", "!keep.log", "/root-only", "build/", "docs/*.md", "!docs/README.md",
			"**/tmp", "cache/**", "a/**/z", "foo**/bar", "x[!0-9]y", "data[[:digit:]]",
			"lit\\*", "\\#hash", "\\!bang", "trail   ", "space\\ ", "out/*", "!out/keep",
			"nested/deep/", "q?q", "[", "*.[oa]", "/anch/*.txt", "**/sub/**/leaf",
		}, "\n") + "\n",
		"sub/":         "!*.log\nlocal\n/anch2\n",
		"sub/inner/":   "*.txt\n!want.txt\n",
		"build/inner/": "never\n",
	}
	paths := []struct {
		path  string
		isDir bool
	}{
		{"a.log", false}, {"keep.log", false}, {"sub/a.log", false}, {"root-only", false},
		{"sub/root-only", false}, {"build", true}, {"build/inner/file", false}, {"docs/a.md", false},
		{"docs/README.md", false}, {"docs/deep/a.md", false}, {"tmp", true}, {"x/tmp", false},
		{"cache/a/b", false}, {"cache", true}, {"a/z", false}, {"a/b/c/z", false},
		{"foo/a/b/bar", false}, {"foox/bar", false}, {"xay", false}, {"x1y", false},
		{"data7", false}, {"datax", false}, {"lit*", false}, {"#hash", false}, {"!bang", false},
		{"trail", false}, {"space ", false}, {"out/keep", false}, {"out/drop", false},
		{"nested/deep", true}, {"x/nested/deep", true}, {"qaq", false}, {"q/q", false},
		{"[", false}, {"m.o", false}, {"m.c", false}, {"anch/a.txt", false}, {"anch/d/a.txt", false},
		{"sub/local", false}, {"local", false}, {"sub/anch2", false}, {"sub/x/anch2", false},
		{"sub/inner/a.txt", false}, {"sub/inner/want.txt", false}, {"y/sub/m/n/leaf", false},
		{"sub/leaf", false},
	}

	repo := t.TempDir()
	// No global excludes: an empty file outside the repository.
	noExcludes := filepath.Join(t.TempDir(), "excludes")
	if err := os.WriteFile(noExcludes, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command(gitBin, append([]string{"-c", "core.ignorecase=false", "-c", "core.excludesFile=" + noExcludes}, args...)...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+repo, "XDG_CONFIG_HOME="+repo)
		out, err := cmd.Output()
		return string(out), err
	}
	if _, err := run("init", "-q"); err != nil {
		t.Skipf("git init: %v", err)
	}
	for folder, content := range files {
		dir := filepath.Join(repo, filepath.FromSlash(folder))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var stdin strings.Builder
	for _, p := range paths {
		full := filepath.Join(repo, filepath.FromSlash(p.path))
		if p.isDir {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, nil, 0o644); err != nil {
				// Some names cannot exist on this filesystem.
				t.Logf("skip %q: %v", p.path, err)
				continue
			}
		}
		stdin.WriteString(p.path + "\n")
	}

	cmd := exec.Command(gitBin, "-c", "core.ignorecase=false", "-c", "core.excludesFile="+noExcludes, "check-ignore", "--no-index", "--stdin")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+repo, "XDG_CONFIG_HOME="+repo)
	cmd.Stdin = strings.NewReader(stdin.String())
	out, err := cmd.Output()
	if err != nil {
		// Exit 1 means nothing matched; anything else is git failing.
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
			t.Skipf("git check-ignore: %v", err)
		}
	}
	gitSays := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			gitSays[line] = true
		}
	}

	m := matcherFor(files)
	for _, p := range paths {
		if !strings.Contains(stdin.String(), p.path+"\n") {
			continue
		}
		if got := m.Excluded(p.path, p.isDir); got != gitSays[p.path] {
			t.Errorf("%q (dir=%v): matcher says excluded=%v, git says %v", p.path, p.isDir, got, gitSays[p.path])
		}
	}
}
