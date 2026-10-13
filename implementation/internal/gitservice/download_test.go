package gitservice

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadRawBlobStreamsExactBytes(t *testing.T) {
	svc, _, _, ctx := browseFixture(t)
	info, err := svc.ResolveRawBlob(ctx, testID, "browse", "docs/guide.md")
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "guide.md" {
		t.Fatalf("name: got %q", info.Name)
	}
	const expected = "# Guide\n\nReadable text.\n"
	if info.Size != int64(len(expected)) {
		t.Fatalf("size: got %d, want %d", info.Size, len(expected))
	}
	var out bytes.Buffer
	if err := svc.StreamBlob(ctx, testID, info.BlobSHA, &out); err != nil {
		t.Fatal(err)
	}
	// A download must be byte-exact: the browser view may reformat, this may not.
	if out.String() != expected {
		t.Fatalf("body: got %q, want %q", out.String(), expected)
	}
}

// A binary file downloads even though the browser view refuses to render it.
// The two paths have different jobs: one shows text, the other returns bytes.
func TestDownloadRawBlobAllowsBinaryContent(t *testing.T) {
	svc, _, _, ctx := browseFixture(t)
	info, err := svc.ResolveRawBlob(ctx, testID, "browse", "binary.bin")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := svc.StreamBlob(ctx, testID, info.BlobSHA, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), []byte{0, 1, 2, 3}) {
		t.Fatalf("binary body: got %v", out.Bytes())
	}
}

func TestDownloadRawBlobRefusesSymlink(t *testing.T) {
	svc, _, _, ctx := browseFixture(t)
	// The link target lives outside the repository; following it would turn a
	// download endpoint into an arbitrary file read.
	if _, err := svc.ResolveRawBlob(ctx, testID, "browse", "docs/secret-link"); !errors.Is(err, ErrBrowseInvalid) {
		t.Fatalf("symlink download: got %v, want ErrBrowseInvalid", err)
	}
}

func TestDownloadRawBlobRefusesTraversalAndMissingPaths(t *testing.T) {
	svc, _, _, ctx := browseFixture(t)
	for _, path := range []string{"../secret", "docs/../../etc/passwd", "/etc/passwd", "docs/"} {
		if _, err := svc.ResolveRawBlob(ctx, testID, "browse", path); !errors.Is(err, ErrBrowseInvalid) {
			t.Fatalf("path %q: got %v, want ErrBrowseInvalid", path, err)
		}
	}
	if _, err := svc.ResolveRawBlob(ctx, testID, "browse", "docs/absent.txt"); !errors.Is(err, ErrBrowseNotFound) {
		t.Fatalf("missing path: got %v, want ErrBrowseNotFound", err)
	}
	if _, err := svc.ResolveRawBlob(ctx, testID, "nope", "docs/guide.md"); !errors.Is(err, ErrBrowseNotFound) {
		t.Fatalf("missing branch: got %v, want ErrBrowseNotFound", err)
	}
}

func TestDownloadRawBlobRefusesOversizedFile(t *testing.T) {
	svc, repo, work, ctx := mergeFixture(t)
	gitCommand(t, ctx, work, "checkout", "-b", "huge")
	// One byte past the limit, so the boundary itself is what is being tested.
	if err := os.WriteFile(filepath.Join(work, "huge.txt"), []byte(strings.Repeat("x", maxRawBlobBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, ctx, work, "add", "huge.txt")
	gitCommand(t, ctx, work, "commit", "-m", "add an oversized file")
	gitCommand(t, ctx, work, "push", repo, "HEAD:refs/heads/huge")
	if _, err := svc.ResolveRawBlob(ctx, testID, "huge", "huge.txt"); !errors.Is(err, ErrDownloadTooLarge) {
		t.Fatalf("oversized download: got %v, want ErrDownloadTooLarge", err)
	}
}

func TestDownloadRawBlobRejectsForgedObjectID(t *testing.T) {
	svc, _, _, ctx := browseFixture(t)
	var out bytes.Buffer
	// The handler passes an object ID back to the service, so the service must
	// still refuse anything that is not a plain object ID.
	for _, sha := range []string{"", "--upload-pack=touch /tmp/pwned", "HEAD", strings.Repeat("f", 39)} {
		if err := svc.StreamBlob(ctx, testID, sha, &out); !errors.Is(err, ErrBrowseInvalid) {
			t.Fatalf("sha %q: got %v, want ErrBrowseInvalid", sha, err)
		}
	}
}

func TestDownloadArchiveZipContainsRevision(t *testing.T) {
	svc, _, _, ctx := browseFixture(t)
	info, err := svc.ResolveArchive(ctx, testID, "browse")
	if err != nil {
		t.Fatal(err)
	}
	if !validSHA(info.CommitSHA) {
		t.Fatalf("archive commit: %q", info.CommitSHA)
	}
	var out bytes.Buffer
	if err := svc.StreamArchive(ctx, testID, info.CommitSHA, ArchiveZip, ArchivePrefix("demo", "browse"), &out); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatalf("archive is not a readable zip: %v", err)
	}
	names := make([]string, 0, len(reader.File))
	for _, file := range reader.File {
		names = append(names, file.Name)
		// Repository metadata must never leave the server inside a download.
		if strings.Contains(file.Name, ".git/") {
			t.Fatalf("archive contains repository metadata: %s", file.Name)
		}
	}
	joined := strings.Join(names, "\n")
	for _, want := range []string{"demo-browse/docs/guide.md", "demo-browse/binary.bin"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("archive missing %q; entries:\n%s", want, joined)
		}
	}
}

func TestDownloadArchiveSupportsTarGzAndRejectsUnknownFormat(t *testing.T) {
	svc, _, _, ctx := browseFixture(t)
	info, err := svc.ResolveArchive(ctx, testID, "browse")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := svc.StreamArchive(ctx, testID, info.CommitSHA, ArchiveTarGz, "", &out); err != nil {
		t.Fatal(err)
	}
	// gzip magic number, so the stream really is gzipped rather than an error page.
	if out.Len() < 2 || out.Bytes()[0] != 0x1f || out.Bytes()[1] != 0x8b {
		t.Fatalf("tar.gz archive lacks a gzip header: %v", out.Bytes()[:min(2, out.Len())])
	}
	out.Reset()
	// An unknown format must be refused here, not passed to the Git command line.
	if err := svc.StreamArchive(ctx, testID, info.CommitSHA, "rar; rm -rf /", "", &out); !errors.Is(err, ErrBrowseInvalid) {
		t.Fatalf("unknown format: got %v, want ErrBrowseInvalid", err)
	}
	if err := svc.StreamArchive(ctx, testID, "not-a-sha", ArchiveZip, "", &out); !errors.Is(err, ErrBrowseInvalid) {
		t.Fatalf("forged revision: got %v, want ErrBrowseInvalid", err)
	}
}

func TestDownloadArchiveRejectsMissingBranch(t *testing.T) {
	svc, _, _, ctx := browseFixture(t)
	if _, err := svc.ResolveArchive(ctx, testID, "absent"); !errors.Is(err, ErrBrowseNotFound) {
		t.Fatalf("missing branch: got %v, want ErrBrowseNotFound", err)
	}
	if _, err := svc.ResolveArchive(ctx, testID, "--upload-pack=oops"); !errors.Is(err, ErrBrowseInvalid) {
		t.Fatalf("option-like branch: got %v, want ErrBrowseInvalid", err)
	}
}

func TestArchivePrefixIsFilesystemSafe(t *testing.T) {
	cases := []struct{ repo, ref, want string }{
		{"demo", "main", "demo-main/"},
		{"my repo", "feature/x", "my-repo-feature-x/"},
		{"", "", "repository-archive/"},
		{"a/../../etc", "main", "a-.-.-etc-main/"},
		{"safe.name_1", "v1.2.3", "safe.name_1-v1.2.3/"},
	}
	for _, c := range cases {
		if got := ArchivePrefix(c.repo, c.ref); got != c.want {
			t.Errorf("ArchivePrefix(%q, %q): got %q, want %q", c.repo, c.ref, got, c.want)
		}
	}
	// A prefix that escapes its directory would let an extractor write outside
	// the intended folder, so no result may contain a bare traversal segment.
	for _, c := range cases {
		if strings.Contains(ArchivePrefix(c.repo, c.ref), "..") {
			t.Errorf("prefix for %q/%q still contains a traversal segment", c.repo, c.ref)
		}
	}
}

func TestDownloadRawBlobHonoursDeadline(t *testing.T) {
	svc, _, _, ctx := browseFixture(t)
	deadline, cancel := context.WithTimeout(ctx, 0)
	defer cancel()
	if _, err := svc.ResolveRawBlob(deadline, testID, "browse", "docs/guide.md"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired deadline: got %v, want context.DeadlineExceeded", err)
	}
}
