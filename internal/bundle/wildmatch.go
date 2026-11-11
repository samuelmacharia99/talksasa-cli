package bundle

import "strings"

// wildmatch is a port of git's wildmatch.c (the matcher behind .gitignore),
// without WM_CASEFOLD: patterns match case-sensitively, as git does with
// core.ignoreCase off.
//
// With wmPathname, "*", "?" and bracket classes never match "/", and "**"
// between slashes (or at either end) matches any number of folders. Without
// it, "*" and "**" are the same and match "/" too; that mode is used for a
// pattern with no slash, which is matched against a base name only.

const wmPathname = 1

const (
	wmMatch           = 0
	wmNoMatch         = 1
	wmAbortAll        = -1
	wmAbortToStarStar = -2
)

func wildmatch(pattern, text string, flags int) bool {
	return dowild(pattern, text, flags) == wmMatch
}

// at is s[i], or 0 past the end: the C code reads the string's NUL there.
// A path never holds a NUL byte, so 0 always means "the end".
func at(s string, i int) byte {
	if i < len(s) {
		return s[i]
	}
	return 0
}

func isGlobSpecial(c byte) bool {
	return c == '*' || c == '?' || c == '[' || c == '\\'
}

func dowild(p, text string, flags int) int {
	pi, ti := 0, 0
	for ; pi < len(p); pi, ti = pi+1, ti+1 {
		pch := p[pi]
		tch := at(text, ti)
		if tch == 0 && pch != '*' {
			return wmAbortAll
		}
		switch pch {
		case '\\':
			// A literal match with the next character. A trailing
			// backslash compares 0 with a text byte, which fails.
			pi++
			if tch != at(p, pi) {
				return wmNoMatch
			}
			continue
		default:
			if tch != pch {
				return wmNoMatch
			}
			continue
		case '?':
			if flags&wmPathname != 0 && tch == '/' {
				return wmNoMatch
			}
			continue
		case '*':
			var matchSlash bool
			pi++
			if at(p, pi) == '*' {
				prevP := pi - 2
				for {
					pi++
					if at(p, pi) != '*' {
						break
					}
				}
				switch {
				case flags&wmPathname == 0:
					// Without WM_PATHNAME, "*" and "**" are the same.
					matchSlash = true
				case (prevP < 0 || p[prevP] == '/') &&
					(at(p, pi) == 0 || at(p, pi) == '/' || (at(p, pi) == '\\' && at(p, pi+1) == '/')):
					// "**/" can match nothing at all: "foo/**/bar"
					// matches "foo/bar" as well as "foo/a/bar".
					if at(p, pi) == '/' && dowild(p[pi+1:], text[ti:], flags) == wmMatch {
						return wmMatch
					}
					matchSlash = true
				default:
					// "**" not between slashes is a single "*".
					matchSlash = false
				}
			} else {
				matchSlash = flags&wmPathname == 0
			}

			if pi >= len(p) {
				// A trailing "**" matches everything; a trailing "*"
				// only what has no slash left.
				if !matchSlash && strings.IndexByte(text[ti:], '/') >= 0 {
					return wmNoMatch
				}
				return wmMatch
			} else if !matchSlash && p[pi] == '/' {
				// One "*" then a slash matches up to the next slash,
				// which the loop's increment then consumes on both sides.
				slash := strings.IndexByte(text[ti:], '/')
				if slash < 0 {
					return wmNoMatch
				}
				ti += slash
				continue
			}

			for {
				if tch == 0 {
					break
				}
				// When a literal follows the star, skip ahead to where
				// it occurs: what comes before it belongs to the star.
				if !isGlobSpecial(at(p, pi)) {
					pch = p[pi]
					for {
						tch = at(text, ti)
						if tch == 0 || (!matchSlash && tch == '/') || tch == pch {
							break
						}
						ti++
					}
					if tch != pch {
						if matchSlash {
							return wmAbortAll
						}
						return wmAbortToStarStar
					}
				}
				matched := dowild(p[pi:], text[ti:], flags)
				if matched != wmNoMatch {
					if !matchSlash || matched != wmAbortToStarStar {
						return matched
					}
				} else if !matchSlash && tch == '/' {
					return wmAbortToStarStar
				}
				ti++
				tch = at(text, ti)
			}
			return wmAbortAll
		case '[':
			pi++
			pch = at(p, pi)
			if pch == '^' {
				pch = '!'
			}
			negated := pch == '!'
			if negated {
				pi++
				pch = at(p, pi)
			}
			var prevCh byte
			matched := false
			for {
				if pch == 0 {
					return wmAbortAll
				}
				switch {
				case pch == '\\':
					pi++
					pch = at(p, pi)
					if pch == 0 {
						return wmAbortAll
					}
					if tch == pch {
						matched = true
					}
				case pch == '-' && prevCh != 0 && at(p, pi+1) != 0 && at(p, pi+1) != ']':
					pi++
					pch = p[pi]
					if pch == '\\' {
						pi++
						pch = at(p, pi)
						if pch == 0 {
							return wmAbortAll
						}
					}
					if tch <= pch && tch >= prevCh {
						matched = true
					}
					pch = 0 // so the next character is not a range end
				case pch == '[' && at(p, pi+1) == ':':
					pi += 2
					start := pi
					for at(p, pi) != 0 && p[pi] != ']' {
						pi++
					}
					if at(p, pi) == 0 {
						return wmAbortAll
					}
					if pi-start-1 < 0 || p[pi-1] != ':' {
						// No ":]": the "[" is an ordinary member.
						pi = start - 2
						pch = '['
						if tch == pch {
							matched = true
						}
						break
					}
					in, ok := inClass(p[start:pi-1], tch)
					if !ok {
						return wmAbortAll // a malformed [:class:]
					}
					if in {
						matched = true
					}
					pch = 0
				default:
					if tch == pch {
						matched = true
					}
				}
				prevCh = pch
				pi++
				pch = at(p, pi)
				if pch == ']' {
					break
				}
			}
			if matched == negated || (flags&wmPathname != 0 && tch == '/') {
				return wmNoMatch
			}
			continue
		}
	}
	if ti < len(text) {
		return wmNoMatch
	}
	return wmMatch
}

// inClass reports whether c is in a POSIX character class, with git's
// ASCII-only definitions. ok is false for an unknown class name.
func inClass(name string, c byte) (in, ok bool) {
	isUpper := c >= 'A' && c <= 'Z'
	isLower := c >= 'a' && c <= 'z'
	isDigit := c >= '0' && c <= '9'
	isAlpha := isUpper || isLower
	isPrint := c >= 0x20 && c <= 0x7e
	isSpace := c == ' ' || c == '\t' || c == '\n' || c == '\v' || c == '\f' || c == '\r'
	switch name {
	case "alnum":
		return isAlpha || isDigit, true
	case "alpha":
		return isAlpha, true
	case "blank":
		return c == ' ' || c == '\t', true
	case "cntrl":
		return c < 0x20 || c == 0x7f, true
	case "digit":
		return isDigit, true
	case "graph":
		return isPrint && c != ' ', true
	case "lower":
		return isLower, true
	case "print":
		return isPrint, true
	case "punct":
		return isPrint && c != ' ' && !isAlpha && !isDigit, true
	case "space":
		return isSpace, true
	case "upper":
		return isUpper, true
	case "xdigit":
		return isDigit || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F'), true
	}
	return false, false
}
