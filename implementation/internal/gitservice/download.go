package gitservice

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Download limits and formats.
//
// A raw download exists so a person can take one file away, not so a client can
// mirror a repository, so it stays bounded: an oversized file is refused with a
// clear error instead of being streamed until the server or the connection gives
// out. Whole-repository transfer is Git's job, not this endpoint's.
const maxRawBlobBytes = 20 << 20

// Archive formats the download endpoint accepts. Anything else is refused here
// rather than passed through to Git.
const (
	ArchiveZip   = "zip"
	ArchiveTarGz = "tar.gz"
)

var ErrDownloadTooLarge = errors.New("file exceeds the download limit")

type RawBlobInfo struct {
	Name      string
	Size      int64
	BlobSHA   string
	CommitSHA string
}

type ArchiveInfo struct {
	CommitSHA string
}

// ResolveRawBlob locates one file at a ref and reports its metadata without
// reading its contents. Resolution and streaming are separate calls so the HTTP
// layer can answer 404 or "too large" before it has committed to a status line:
// once the first body byte is written, an error can no longer be reported.
func (s *Service) ResolveRawBlob(ctx context.Context, id, ref, path string) (RawBlobInfo, error) {
	parts, err := browsePathParts(path, false)
	if err != nil {
		return RawBlobInfo{}, err
	}
	repo, err := s.Path(id)
	if err != nil {
		return RawBlobInfo{}, err
	}
	if err = s.rejectBrowseUnsafeConfig(ctx, repo); err != nil {
		return RawBlobInfo{}, err
	}
	commit, err := s.resolveBrowseBranch(ctx, repo, ref)
	if err != nil {
		return RawBlobInfo{}, err
	}
	target, err := s.resolveBlobRecord(ctx, repo, commit, parts)
	if err != nil {
		return RawBlobInfo{}, err
	}
	sizeBytes, err := s.browseGit(ctx, repo, 128, "cat-file", "-s", string(target.oid))
	if err != nil {
		if ctx.Err() != nil {
			return RawBlobInfo{}, ctx.Err()
		}
		return RawBlobInfo{}, ErrBrowseOperation
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(sizeBytes)), 10, 64)
	if err != nil || size < 0 {
		return RawBlobInfo{}, ErrBrowseOperation
	}
	if size > maxRawBlobBytes {
		return RawBlobInfo{}, ErrDownloadTooLarge
	}
	return RawBlobInfo{Name: parts[len(parts)-1], Size: size, BlobSHA: string(target.oid), CommitSHA: commit}, nil
}

// StreamBlob copies one blob's bytes to the writer. The object ID is expected to
// come from ResolveRawBlob, but it is re-validated here anyway: this is the
// boundary where a caller-supplied value would otherwise become a Git argument.
func (s *Service) StreamBlob(ctx context.Context, id, blobSHA string, destination io.Writer) error {
	if !validSHA(blobSHA) {
		return ErrBrowseInvalid
	}
	repo, err := s.Path(id)
	if err != nil {
		return err
	}
	if err = s.rejectBrowseUnsafeConfig(ctx, repo); err != nil {
		return err
	}
	return s.streamGit(ctx, repo, destination, "cat-file", "blob", blobSHA)
}

// ResolveArchive pins a ref to the commit the archive will contain. Resolving
// first means the bytes and the file name the user sees describe the same
// immutable revision even if the branch moves while the archive is produced.
func (s *Service) ResolveArchive(ctx context.Context, id, ref string) (ArchiveInfo, error) {
	repo, err := s.Path(id)
	if err != nil {
		return ArchiveInfo{}, err
	}
	if err = s.rejectBrowseUnsafeConfig(ctx, repo); err != nil {
		return ArchiveInfo{}, err
	}
	commit, err := s.resolveBrowseBranch(ctx, repo, ref)
	if err != nil {
		return ArchiveInfo{}, err
	}
	return ArchiveInfo{CommitSHA: commit}, nil
}

// StreamArchive writes an archive of one commit. Git produces it, so the .git
// directory is never included and no working tree is ever created on the server.
// The format is matched against a fixed set: an arbitrary string must never
// reach the command line.
func (s *Service) StreamArchive(ctx context.Context, id, commit, format, prefix string, destination io.Writer) error {
	if !validSHA(commit) {
		return ErrBrowseInvalid
	}
	var formatFlag string
	switch format {
	case ArchiveZip:
		formatFlag = "--format=zip"
	case ArchiveTarGz:
		formatFlag = "--format=tar.gz"
	default:
		return ErrBrowseInvalid
	}
	repo, err := s.Path(id)
	if err != nil {
		return err
	}
	if err = s.rejectBrowseUnsafeConfig(ctx, repo); err != nil {
		return err
	}
	args := []string{"archive", formatFlag}
	if prefix != "" {
		args = append(args, "--prefix="+prefix)
	}
	args = append(args, commit)
	return s.streamGit(ctx, repo, destination, args...)
}

// ArchivePrefix builds the directory an extracted archive lands in, mirroring
// the convention users expect from other forges. Characters that would confuse
// an extractor are replaced rather than escaped, and the result always ends in a
// slash because Git requires it.
//
// Replacing separators already prevents traversal, since ".." can only escape a
// directory when it is its own path component. Consecutive dots are collapsed
// anyway: an extractor on another platform may split paths differently, and a
// name containing ".." should never reach one.
func ArchivePrefix(repo, ref string) string {
	safe := func(value string) string {
		var b strings.Builder
		lastDot := false
		for _, r := range value {
			switch {
			case r == '.':
				if lastDot {
					continue
				}
				lastDot = true
				b.WriteRune(r)
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
				lastDot = false
				b.WriteRune(r)
			default:
				lastDot = false
				b.WriteRune('-')
			}
		}
		return b.String()
	}
	name := safe(repo)
	if name == "" {
		name = "repository"
	}
	branch := safe(ref)
	if branch == "" {
		branch = "archive"
	}
	return name + "-" + branch + "/"
}

// streamGit runs a read-only Git command whose standard output is copied
// straight to the caller's writer instead of being buffered, so a large object
// never has to fit in memory. Standard error is discarded and never reaches the
// client, and repository-specific hooks or attributes cannot run.
func (s *Service) streamGit(ctx context.Context, repo string, destination io.Writer, args ...string) error {
	all := append([]string{"-c", "core.hooksPath=" + os.DevNull, "-c", "core.attributesFile=" + os.DevNull}, args...)
	command := exec.CommandContext(ctx, s.git, all...)
	command.Dir = repo
	command.Env = append(gitEnv(s.git), "GIT_NO_REPLACE_OBJECTS=1", "GIT_ATTR_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	command.Stdout = destination
	command.Stderr = ioDiscard{}
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrBrowseOperation
	}
	return nil
}
