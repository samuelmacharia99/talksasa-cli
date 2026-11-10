// Package dotenv reads KEY=value files for `env set --from-file`.
//
// It accepts what .env files commonly hold: comments, blank lines, an
// `export ` prefix, single quotes (literal), double quotes (with \n, \t, \"
// and \\ escapes, and values spanning lines), and unquoted values with a
// trailing ` # comment`. It does not expand ${VARIABLES}: a value is sent as
// written, and the app reads it as written.
package dotenv

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// Var is one KEY=value in file order.
type Var struct {
	Key   string
	Value string
	Line  int
}

var keyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)

// Parse reads every assignment. A later assignment of a key replaces an
// earlier one, as in a shell.
func Parse(r io.Reader) ([]Var, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var lines []string
	for scanner.Scan() {
		lines = append(lines, strings.TrimSuffix(scanner.Text(), "\r"))
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	var out []Var
	index := map[string]int{}
	for i := 0; i < len(lines); i++ {
		lineNo := i + 1
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")

		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			return nil, fmt.Errorf("line %d: expected KEY=value", lineNo)
		}
		key := strings.TrimSpace(line[:eq])
		if !keyPattern.MatchString(key) {
			return nil, fmt.Errorf("line %d: %q is not a variable name", lineNo, key)
		}
		rest := strings.TrimLeft(line[eq+1:], " \t")

		var value string
		switch {
		case strings.HasPrefix(rest, `"`):
			v, consumed, err := doubleQuoted(rest[1:], lines[i+1:])
			if err != nil {
				return nil, fmt.Errorf("line %d: %v", lineNo, err)
			}
			value = v
			i += consumed
		case strings.HasPrefix(rest, "'"):
			end := strings.IndexByte(rest[1:], '\'')
			if end < 0 {
				return nil, fmt.Errorf("line %d: unclosed single quote", lineNo)
			}
			value = rest[1 : 1+end]
		default:
			if hash := strings.Index(rest, " #"); hash >= 0 {
				rest = rest[:hash]
			}
			value = strings.TrimSpace(rest)
		}

		if at, ok := index[key]; ok {
			out[at] = Var{Key: key, Value: value, Line: lineNo}
			continue
		}
		index[key] = len(out)
		out = append(out, Var{Key: key, Value: value, Line: lineNo})
	}
	return out, nil
}

// doubleQuoted reads a double-quoted value that may continue onto the
// following lines; consumed is how many of them it used.
func doubleQuoted(first string, following []string) (value string, consumed int, err error) {
	var b strings.Builder
	text := first
	for {
		for j := 0; j < len(text); j++ {
			c := text[j]
			switch {
			case c == '\\' && j+1 < len(text):
				j++
				switch text[j] {
				case 'n':
					b.WriteByte('\n')
				case 't':
					b.WriteByte('\t')
				case 'r':
					b.WriteByte('\r')
				case '"', '\\', '$':
					b.WriteByte(text[j])
				default:
					b.WriteByte('\\')
					b.WriteByte(text[j])
				}
			case c == '"':
				return b.String(), consumed, nil
			default:
				b.WriteByte(c)
			}
		}
		if consumed >= len(following) {
			return "", 0, fmt.Errorf("unclosed double quote")
		}
		b.WriteByte('\n')
		text = following[consumed]
		consumed++
	}
}
