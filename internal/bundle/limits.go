package bundle

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// LimitKind names the platform limit a folder passed.
type LimitKind string

const (
	LimitFiles      LimitKind = "files"
	LimitFolders    LimitKind = "folders"
	LimitDepth      LimitKind = "depth"
	LimitUnpacked   LimitKind = "unpacked_bytes"
	LimitCompressed LimitKind = "compressed_bytes"
)

// LimitError is a folder over one of the platform's limits. Its message
// names the largest top-level folders and a .talksasaignore line to try.
type LimitError struct {
	Kind LimitKind
	Max  int64
	// Actual is the size that passed the limit, for the byte limits found
	// while writing; the counts below are the walk's totals.
	Actual   int64
	Files    int
	Folders  int
	Unpacked int64
	// AtLeast is set when counting stopped early, so the totals are
	// lower bounds.
	AtLeast bool
	// Path is the entry nested too deep (LimitDepth).
	Path string
	Top  []TopLevel
}

func (e *LimitError) Error() string {
	var b strings.Builder
	atLeast := ""
	if e.AtLeast {
		atLeast = "at least "
	}
	switch e.Kind {
	case LimitFiles:
		fmt.Fprintf(&b, "the folder has %s%s files; the platform takes at most %s.", atLeast, thousands(int64(e.Files)), thousands(e.Max))
	case LimitFolders:
		fmt.Fprintf(&b, "the folder has %s%s folders; the platform takes at most %s.", atLeast, thousands(int64(e.Folders)), thousands(e.Max))
	case LimitDepth:
		fmt.Fprintf(&b, "%s is nested %d levels deep; the platform takes at most %d.", shorten(e.Path), strings.Count(e.Path, "/")+1, e.Max)
	case LimitUnpacked:
		size := e.Unpacked
		if e.Actual > size {
			size = e.Actual
		}
		fmt.Fprintf(&b, "the files come to %s%s; the platform takes at most %s unpacked.", atLeast, HumanBytes(size), HumanBytes(e.Max))
	case LimitCompressed:
		fmt.Fprintf(&b, "the bundle comes to %s%s compressed; the platform takes at most %s.", atLeast, HumanBytes(e.Actual), HumanBytes(e.Max))
	}

	if e.Kind == LimitDepth {
		first, _, _ := strings.Cut(e.Path, "/")
		fmt.Fprintf(&b, "\nLeave it out with a line in .talksasaignore, for example:\n  /%s/", first)
		return b.String()
	}

	top := e.largest(5)
	if len(top) == 0 {
		return b.String()
	}
	by := "size"
	if e.Kind == LimitFiles || e.Kind == LimitFolders {
		by = "count"
	}
	fmt.Fprintf(&b, "\nLargest top-level folders by %s:", by)
	width := 0
	for _, t := range top {
		if len(t.Name) > width {
			width = len(t.Name)
		}
	}
	for _, t := range top {
		fmt.Fprintf(&b, "\n  %-*s  %s files, %s folders, %s", width+1, t.Name+"/", thousands(int64(t.Files)), thousands(int64(t.Folders)), HumanBytes(t.Bytes))
	}
	fmt.Fprintf(&b, "\nLeave a folder out with a line in .talksasaignore, for example:\n  /%s/", top[0].Name)
	return b.String()
}

// largest is the n biggest top-level folders for this limit.
func (e *LimitError) largest(n int) []TopLevel {
	var folders []TopLevel
	for _, t := range e.Top {
		if t.Folder {
			folders = append(folders, t)
		}
	}
	key := func(t TopLevel) int64 {
		switch e.Kind {
		case LimitFiles:
			return int64(t.Files)
		case LimitFolders:
			return int64(t.Folders)
		}
		return t.Bytes
	}
	sort.SliceStable(folders, func(i, j int) bool {
		ki, kj := key(folders[i]), key(folders[j])
		if ki != kj {
			return ki > kj
		}
		return folders[i].Name < folders[j].Name
	})
	if len(folders) > n {
		folders = folders[:n]
	}
	return folders
}

// HumanBytes formats a size with binary units: 1.5 MiB.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// thousands formats 50000 as 50,000.
func thousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
