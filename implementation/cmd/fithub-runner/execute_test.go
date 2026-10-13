package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeArchive builds a gzipped tar from the given entries.
func makeArchive(t *testing.T, entries []tar.Header, contents []string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for index, header := range entries {
		item := header
		if item.Typeflag == 0 {
			item.Typeflag = tar.TypeReg
		}
		if err := tarWriter.WriteHeader(&item); err != nil {
			t.Fatal(err)
		}
		if index < len(contents) && contents[index] != "" {
			if _, err := tarWriter.Write([]byte(contents[index])); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestExtractTarGzWritesRegularFiles(t *testing.T) {
	root := t.TempDir()
	archive := makeArchive(t, []tar.Header{
		{Name: "README.md", Mode: 0o644, Size: int64(len("hello\n"))},
		{Name: "internal/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "internal/a.go", Mode: 0o644, Size: int64(len("package a\n"))},
		{Name: "script.sh", Mode: 0o755, Size: int64(len("#!/bin/sh\n"))},
	}, []string{"hello\n", "", "package a\n", "#!/bin/sh\n"})
	if err := extractTarGz(bytes.NewReader(archive), root); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil || string(content) != "hello\n" {
		t.Fatalf("README.md = %q err=%v", content, err)
	}
	nested, err := os.ReadFile(filepath.Join(root, "internal", "a.go"))
	if err != nil || string(nested) != "package a\n" {
		t.Fatalf("internal/a.go = %q err=%v", nested, err)
	}
	scriptInfo, err := os.Stat(filepath.Join(root, "script.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if got := scriptInfo.Mode().Perm(); got != 0o755 {
		t.Fatalf("script.sh mode = %04o, want 0755", got)
	}
}

func TestMakeContainerWorkspaceReadable(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	page := filepath.Join(repo, "index.html")
	if err := os.WriteFile(page, []byte("demo"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(repo, "deploy.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := makeContainerWorkspaceReadable(root); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		mode os.FileMode
	}{
		{root, 0o755},
		{repo, 0o755},
		{page, 0o644},
		{script, 0o755},
	} {
		info, err := os.Stat(tc.path)
		if err != nil {
			t.Errorf("stat %s: %v", tc.path, err)
			continue
		}
		if got := info.Mode().Perm(); got != tc.mode {
			t.Errorf("%s mode = %04o, want %04o", tc.path, got, tc.mode)
		}
	}
}

// `git archive` prefixes its stream with a pax metadata entry. Treating that as
// an unknown entry type would reject every real archive, so it must be skipped
// while the real files around it are still extracted.
func TestExtractTarGzSkipsPaxMetadata(t *testing.T) {
	root := t.TempDir()
	archive := makeArchive(t, []tar.Header{
		{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader},
		{Name: "README.md", Mode: 0o644, Size: int64(len("hello\n"))},
	}, []string{"", "hello\n"})
	if err := extractTarGz(bytes.NewReader(archive), root); err != nil {
		t.Fatalf("pax metadata entry was rejected: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil || string(content) != "hello\n" {
		t.Fatalf("README.md = %q err=%v", content, err)
	}
}

// A crafted archive must never write outside the workspace, and a symlink must
// never be created: both are the classic ways to escape a checkout directory.
func TestExtractTarGzRejectsEscapes(t *testing.T) {
	cases := []struct {
		name    string
		header  tar.Header
		content string
	}{
		{"parent traversal", tar.Header{Name: "../escaped.txt", Mode: 0o644, Size: 4}, "evil"},
		{"nested traversal", tar.Header{Name: "a/../../escaped.txt", Mode: 0o644, Size: 4}, "evil"},
		{"absolute path", tar.Header{Name: "/tmp/escaped.txt", Mode: 0o644, Size: 4}, "evil"},
		{"symlink", tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}, ""},
		{"hardlink", tar.Header{Name: "hard", Typeflag: tar.TypeLink, Linkname: "/etc/passwd"}, ""},
		{"fifo", tar.Header{Name: "pipe", Typeflag: tar.TypeFifo}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			archive := makeArchive(t, []tar.Header{tc.header}, []string{tc.content})
			if err := extractTarGz(bytes.NewReader(archive), root); err == nil {
				t.Fatalf("archive entry %q was accepted", tc.header.Name)
			}
			// Nothing may exist outside the root after a rejected archive.
			if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escaped.txt")); err == nil {
				t.Fatal("an entry escaped the workspace")
			}
		})
	}
}

func TestSanitizeLogLineStripsControlCharacters(t *testing.T) {
	// An escape sequence in a log line must not be able to rewrite a terminal
	// that displays the log later.
	line := "normal \x1b[31mred\x1b[0m\x07 bell\ttab"
	got := sanitizeLogLine(line)
	if strings.ContainsAny(got, "\x1b\x07") {
		t.Fatalf("control characters survived: %q", got)
	}
	if !strings.Contains(got, "normal") || !strings.Contains(got, "tab") {
		t.Fatalf("visible text was lost: %q", got)
	}
}

func TestSanitizeLogLineTruncatesLongLines(t *testing.T) {
	got := sanitizeLogLine(strings.Repeat("a", maxLogLineBytes*2))
	if len(got) > maxLogLineBytes+64 {
		t.Fatalf("line was not truncated: %d bytes", len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Fatal("truncation must be visible in the log")
	}
}

func TestSanitizeLogLineReplacesInvalidUTF8(t *testing.T) {
	got := sanitizeLogLine("bad \xff\xfe bytes\n")
	if !strings.Contains(got, "bad") || !strings.HasSuffix(got, "\n") {
		t.Fatalf("unexpected result: %q", got)
	}
	if strings.ContainsRune(got, 0xfffd) == false && !strings.Contains(got, "\ufffd") {
		t.Fatalf("invalid bytes were not replaced: %q", got)
	}
}

func TestStreamLinesStopsWhenContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	lines := make([]string, 0)
	streamLines(ctx, strings.NewReader("one\ntwo\n"), func(line string) { lines = append(lines, line) })
	if len(lines) != 0 {
		t.Fatalf("a cancelled stream produced %d lines", len(lines))
	}
}

func TestLoadConfigRequiresPinnedImage(t *testing.T) {
	for key, value := range map[string]string{
		"FITHUB_RUNNER_SERVER":    "https://git.example.com",
		"FITHUB_RUNNER_TOKEN":     "token",
		"FITHUB_RUNNER_WORK_ROOT": "/tmp/fithub-runner",
		"FITHUB_RUNNER_IMAGE":     "example/runner:latest",
	} {
		t.Setenv(key, value)
	}
	if _, err := loadConfig(); err == nil {
		t.Fatal("a floating image tag must be rejected")
	}
	t.Setenv("FITHUB_RUNNER_IMAGE", "example/runner@sha256:"+strings.Repeat("a", 64))
	if _, err := loadConfig(); err != nil {
		t.Fatalf("a digest-pinned image should be accepted: %v", err)
	}
	t.Setenv("FITHUB_RUNNER_IMAGE", "sha256:"+strings.Repeat("b", 64))
	if _, err := loadConfig(); err != nil {
		t.Fatalf("a local content-addressed image should be accepted: %v", err)
	}
	t.Setenv("FITHUB_RUNNER_IMAGE", "sha256:not-a-digest")
	if _, err := loadConfig(); err == nil {
		t.Fatal("a malformed local image ID must be rejected")
	}
}

func TestLoadConfigRejectsInsecureServer(t *testing.T) {
	for key, value := range map[string]string{
		"FITHUB_RUNNER_SERVER":    "git.example.com",
		"FITHUB_RUNNER_TOKEN":     "token",
		"FITHUB_RUNNER_WORK_ROOT": "/tmp/fithub-runner",
		"FITHUB_RUNNER_IMAGE":     "example/runner@sha256:" + strings.Repeat("a", 64),
	} {
		t.Setenv(key, value)
	}
	if _, err := loadConfig(); err == nil {
		t.Fatal("a server URL without a scheme must be rejected")
	}
}

func TestSanitizeNameProducesContainerSafeName(t *testing.T) {
	got := sanitizeName("ab12-3456/../../evil name")
	if strings.ContainsAny(got, "/ .") {
		t.Fatalf("unsafe container name: %q", got)
	}
	if len(got) > 48 {
		t.Fatalf("name too long: %d", len(got))
	}
	if sanitizeName("") == "" {
		t.Fatal("an empty name must still produce a usable container name")
	}
}
