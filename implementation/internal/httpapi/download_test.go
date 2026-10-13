package httpapi

import (
	"strings"
	"testing"
)

func TestRawContentTypeNeverRendersMarkup(t *testing.T) {
	cases := []struct{ name, want string }{
		// Markup is the interesting case: it must never come back as text/html,
		// or a stored file could run script on this origin when opened.
		{"index.html", "text/plain; charset=utf-8"},
		{"page.htm", "text/plain; charset=utf-8"},
		{"icon.svg", "text/plain; charset=utf-8"},
		{"script.js", "text/plain; charset=utf-8"},
		{"README.md", "text/plain; charset=utf-8"},
		{"main.go", "text/plain; charset=utf-8"},
		{"Makefile", "application/octet-stream"},
		{"archive.zip", "application/octet-stream"},
		{"data.json", "text/plain; charset=utf-8"},
		{"UPPER.HTML", "text/plain; charset=utf-8"},
	}
	for _, c := range cases {
		if got := rawContentType(c.name); got != c.want {
			t.Errorf("rawContentType(%q): got %q, want %q", c.name, got, c.want)
		}
	}
	// Images keep their type so a preview can work, but nothing else is inferred.
	if got := rawContentType("logo.png"); !strings.HasPrefix(got, "image/") {
		t.Errorf("rawContentType(logo.png): got %q, want an image type", got)
	}
}

func TestContentDispositionIsHeaderSafe(t *testing.T) {
	// A file name containing CRLF would let a repository inject response headers.
	injected := contentDisposition("evil\r\nX-Injected: 1")
	if strings.ContainsAny(injected, "\r\n") {
		t.Fatalf("header injection survived: %q", injected)
	}
	if !strings.Contains(injected, "attachment") {
		t.Fatalf("missing attachment disposition: %q", injected)
	}
	// Non-ASCII names must survive in the RFC 5987 form even though the quoted
	// fallback is replaced.
	unicode := contentDisposition("说明.md")
	if !strings.Contains(unicode, "filename*=UTF-8''") {
		t.Fatalf("missing UTF-8 form: %q", unicode)
	}
	if !strings.Contains(unicode, "%E8%AF%B4%E6%98%8E.md") {
		t.Fatalf("UTF-8 name was not encoded as expected: %q", unicode)
	}
	// A quote in the name must not terminate the quoted string early.
	quoted := contentDisposition("a\"b.txt")
	if strings.Count(quoted, `"`) != 2 {
		t.Fatalf("unbalanced quoting: %q", quoted)
	}
}

func TestArchiveNamesAndExtensions(t *testing.T) {
	if got, ok := archiveExtension("zip"); !ok || got != ".zip" {
		t.Fatalf("zip extension: got %q, %v", got, ok)
	}
	if got, ok := archiveExtension("tar.gz"); !ok || got != ".tar.gz" {
		t.Fatalf("tar.gz extension: got %q, %v", got, ok)
	}
	if _, ok := archiveExtension("rar"); ok {
		t.Fatal("an unsupported format was accepted")
	}
	if got := archiveContentType("zip"); got != "application/zip" {
		t.Fatalf("zip content type: %q", got)
	}
	if got := archiveContentType("tar.gz"); got != "application/gzip" {
		t.Fatalf("tar.gz content type: %q", got)
	}
	// The name ends up in a header, so separators and spaces must not survive.
	if got := archiveName("my repo", "feature/x"); got != "my-repo-feature-x" {
		t.Fatalf("archive name: %q", got)
	}
	if got := archiveName("", ""); got != "repository-archive" {
		t.Fatalf("empty archive name: %q", got)
	}
}
