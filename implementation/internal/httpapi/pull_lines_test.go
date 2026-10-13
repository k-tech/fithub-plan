package httpapi

import "testing"

const samplePatch = `diff --git a/internal/a.go b/internal/a.go
index 1111111..2222222 100644
--- a/internal/a.go
+++ b/internal/a.go
@@ -1,4 +1,5 @@
 package a
 
+import "fmt"
 var x = 1
 var y = 2
diff --git a/README.md b/README.md
index 3333333..4444444 100644
--- a/README.md
+++ b/README.md
@@ -1,2 +1,2 @@
-# Old title
+# New title
 body text
`

func TestDiffLineExists(t *testing.T) {
	cases := []struct {
		name string
		path string
		line int
		side string
		want bool
	}{
		{"added line on new side", "internal/a.go", 3, "new", true},
		{"context line new side", "internal/a.go", 4, "new", true},
		{"context line old side", "internal/a.go", 3, "old", true},
		{"old side stops at the old line count", "internal/a.go", 5, "old", false},
		{"new side has one extra line", "internal/a.go", 5, "new", true},
		{"beyond hunk", "internal/a.go", 90, "new", false},
		{"line zero", "internal/a.go", 0, "new", false},
		{"unknown path", "internal/missing.go", 3, "new", false},
		{"invalid side", "internal/a.go", 3, "middle", false},
		{"second file added line", "README.md", 1, "new", true},
		{"second file removed line", "README.md", 1, "old", true},
	}
	for _, tc := range cases {
		if got := diffLineExists(samplePatch, tc.path, tc.line, tc.side); got != tc.want {
			t.Errorf("%s: diffLineExists() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A file whose only change is a deleted first line still has to resolve the old
// side correctly; the new side has no line 0.
func TestDiffLineExistsDeletedFirstLine(t *testing.T) {
	patch := "diff --git a/gone.txt b/gone.txt\n" +
		"--- a/gone.txt\n+++ b/gone.txt\n" +
		"@@ -1,2 +1 @@\n" +
		"-first\n second\n"
	if !diffLineExists(patch, "gone.txt", 1, "old") {
		t.Error("expected old line 1 to exist")
	}
	if diffLineExists(patch, "gone.txt", 0, "new") {
		t.Error("new line 0 must never exist")
	}
}

// A new file is a pure addition; the old side has no lines at all.
func TestDiffLineExistsNewFile(t *testing.T) {
	patch := "diff --git a/new.txt b/new.txt\n" +
		"new file mode 100644\n--- /dev/null\n+++ b/new.txt\n" +
		"@@ -0,0 +1,2 @@\n+one\n+two\n"
	if !diffLineExists(patch, "new.txt", 2, "new") {
		t.Error("expected added line 2")
	}
	if diffLineExists(patch, "new.txt", 1, "old") {
		t.Error("a new file has no old lines")
	}
}

func TestParseDiffPath(t *testing.T) {
	// Git appends metadata after a tab, which must not become part of the path.
	if got := parseDiffPath("b/pkg/x.go\t2024-01-01"); got != "pkg/x.go" {
		t.Errorf("parseDiffPath() = %q, want pkg/x.go", got)
	}
	if got := parseDiffPath("/dev/null"); got != "" {
		t.Errorf("parseDiffPath(/dev/null) = %q, want empty", got)
	}
}
