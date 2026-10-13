package httpapi

import "strings"

// diffLineExists reports whether a unified diff contains the given line on the
// requested side of the given file. Inline comments must anchor to a line the
// reviewer could actually see, so an anchor that is absent from the patch is
// refused instead of being stored and rendered against the wrong code.
//
// The diff is produced by this server with `--no-renames`, so each file appears
// as a single `diff --git a/<path> b/<path>` section and the `+++` header names
// the new path (or /dev/null for a deletion).
func diffLineExists(patch, path string, line int, side string) bool {
	if path == "" || line < 1 || (side != "new" && side != "old") {
		return false
	}
	var (
		currentPath string
		inHunk      bool
		oldLine     int
		newLine     int
	)
	for _, raw := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(raw, "diff --git "):
			currentPath, inHunk = "", false
		case strings.HasPrefix(raw, "+++ "):
			currentPath = parseDiffPath(strings.TrimPrefix(raw, "+++ "))
			inHunk = false
		case strings.HasPrefix(raw, "@@"):
			var ok bool
			oldLine, newLine, ok = parseHunkHeader(raw)
			inHunk = ok
		case !inHunk || currentPath != path:
			continue
		case raw == "":
			// Git always emits a leading character for a real hunk line, so an
			// empty string is the artifact of splitting on the final newline.
			continue
		case raw[0] == '+':
			if side == "new" && newLine == line {
				return true
			}
			newLine++
		case raw[0] == '-':
			if side == "old" && oldLine == line {
				return true
			}
			oldLine++
		case raw[0] == ' ':
			if side == "old" && oldLine == line {
				return true
			}
			if side == "new" && newLine == line {
				return true
			}
			oldLine++
			newLine++
		case raw[0] == '\\':
			// "\ No newline at end of file" carries no line of its own.
		}
	}
	return false
}

// parseDiffPath extracts the repository-relative path from a `+++`/`---` header
// value such as "b/internal/x.go" or "/dev/null".
func parseDiffPath(value string) string {
	value = strings.TrimSpace(value)
	if tab := strings.IndexByte(value, '\t'); tab >= 0 {
		value = value[:tab]
	}
	if value == "/dev/null" {
		return ""
	}
	if strings.HasPrefix(value, "a/") || strings.HasPrefix(value, "b/") {
		return value[2:]
	}
	return value
}

// parseHunkHeader reads "@@ -old,count +new,count @@ optional section" and
// returns the first old and new line numbers.
func parseHunkHeader(line string) (int, int, bool) {
	rest := strings.TrimPrefix(line, "@@")
	end := strings.Index(rest, "@@")
	if end < 0 {
		return 0, 0, false
	}
	fields := strings.Fields(rest[:end])
	if len(fields) < 2 {
		return 0, 0, false
	}
	oldStart, okOld := parseHunkRange(fields[0], '-')
	newStart, okNew := parseHunkRange(fields[1], '+')
	if !okOld || !okNew {
		return 0, 0, false
	}
	return oldStart, newStart, true
}

func parseHunkRange(field string, sign byte) (int, bool) {
	if len(field) < 2 || field[0] != sign {
		return 0, false
	}
	field = field[1:]
	if comma := strings.IndexByte(field, ','); comma >= 0 {
		field = field[:comma]
	}
	value := 0
	for _, char := range field {
		if char < '0' || char > '9' {
			return 0, false
		}
		value = value*10 + int(char-'0')
	}
	return value, true
}
