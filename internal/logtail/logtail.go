// Package logtail finds the new lines between two reads of a log's tail.
//
// The API returns the last N lines with no position, so following a log
// means reading the tail again and printing what follows the overlap with
// the previous read.
package logtail

// New returns the lines of next that come after prev. gap is true when the
// two reads share no lines: more than a read's worth arrived in between (or
// the app restarted and its log began again), so lines may have been missed.
func New(prev, next []string) (fresh []string, gap bool) {
	if len(prev) == 0 {
		return next, false
	}
	if len(next) == 0 {
		return nil, false
	}

	// The longest suffix of prev that is a prefix of next. Longest first, so
	// a run of identical lines is not printed again.
	max := len(prev)
	if len(next) < max {
		max = len(next)
	}
	for k := max; k > 0; k-- {
		if equal(prev[len(prev)-k:], next[:k]) {
			return next[k:], false
		}
	}
	return next, true
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
