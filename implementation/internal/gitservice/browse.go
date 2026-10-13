package gitservice

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxBrowseTreeOutput = 4 << 20
	maxBrowseEntries    = 500
	maxBrowsePathBytes  = 2048
	maxBrowsePathParts  = 64
	maxBrowseBlobBytes  = 512 << 10
	// How far back the file listing looks for each entry's last change. The scan
	// is best-effort garnish on the listing, so it is bounded rather than allowed
	// to walk a long history on every directory view.
	maxLastCommitScan = 200
	maxLastCommitSize = 2 << 20
)

var (
	ErrBrowseInvalid   = errors.New("invalid repository browse input")
	ErrBrowseNotFound  = errors.New("repository path or branch not found")
	ErrBrowseTooLarge  = errors.New("repository browse result exceeds the current limit")
	ErrBrowseUnsafe    = errors.New("repository contains unsafe Git configuration")
	ErrBrowseOperation = errors.New("Git browse operation failed")
)

type BrowseEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
	// LastCommit describes the most recent change to this entry. It is omitted
	// when the scan could not determine one, which is why it is a pointer.
	LastCommit *BrowseCommit `json:"lastCommit,omitempty"`
}

// BrowseCommit is the summary of one commit as shown beside a file.
type BrowseCommit struct {
	SHA         string `json:"sha"`
	Subject     string `json:"subject"`
	AuthorName  string `json:"authorName"`
	CommittedAt string `json:"committedAt"`
}

type TreeResult struct {
	Ref       string        `json:"ref"`
	CommitSHA string        `json:"commitSha"`
	Path      string        `json:"path"`
	Entries   []BrowseEntry `json:"entries"`
	Truncated bool          `json:"truncated"`
}

type BlobResult struct {
	Ref       string `json:"ref"`
	CommitSHA string `json:"commitSha"`
	Path      string `json:"path"`
	BlobSHA   string `json:"blobSha"`
	Size      int64  `json:"size"`
	Text      string `json:"text"`
	Binary    bool   `json:"binary"`
	TooLarge  bool   `json:"tooLarge"`
}

type treeRecord struct{ mode, kind, oid, name []byte }

// BrowseTree resolves a branch to a commit, then walks tree objects by object ID.
// It never checks out files or follows symlink entries from a repository.
func (s *Service) BrowseTree(ctx context.Context, id, branch, path string) (TreeResult, error) {
	parts, err := browsePathParts(path, true)
	if err != nil {
		return TreeResult{}, err
	}
	repo, err := s.Path(id)
	if err != nil {
		return TreeResult{}, err
	}
	if err = s.rejectBrowseUnsafeConfig(ctx, repo); err != nil {
		return TreeResult{}, err
	}
	commit, err := s.resolveBrowseBranch(ctx, repo, branch)
	if err != nil {
		return TreeResult{}, err
	}
	treeOID, err := s.browseCommitTree(ctx, repo, commit)
	if err != nil {
		return TreeResult{}, err
	}
	for _, part := range parts {
		records, listErr := s.readTreeRecords(ctx, repo, treeOID)
		if listErr != nil {
			return TreeResult{}, listErr
		}
		found := false
		for _, rec := range records {
			if string(rec.name) == part && string(rec.kind) == "tree" && string(rec.mode) == "040000" {
				treeOID = string(rec.oid)
				found = true
				break
			}
		}
		if !found {
			return TreeResult{}, ErrBrowseNotFound
		}
	}
	records, err := s.readTreeRecords(ctx, repo, treeOID)
	if err != nil {
		return TreeResult{}, err
	}
	entries := make([]BrowseEntry, 0, min(len(records), maxBrowseEntries))
	for _, rec := range records {
		name := string(rec.name)
		// Git permits names that aren't representable in a URL/JSON browser path.
		// Hide those entries instead of exposing an unaddressable or ambiguous link.
		if !utf8.ValidString(name) || !safeTreeName(name) {
			continue
		}
		kind := "other"
		switch {
		case string(rec.mode) == "040000" && string(rec.kind) == "tree":
			kind = "directory"
		case string(rec.mode) == "100644" || string(rec.mode) == "100755":
			kind = "file"
		case string(rec.mode) == "120000":
			kind = "symlink"
		}
		entryPath := name
		if path != "" {
			entryPath = path + "/" + name
		}
		entries = append(entries, BrowseEntry{Name: name, Path: entryPath, Type: kind, SHA: string(rec.oid)})
	}
	truncated := len(entries) > maxBrowseEntries
	if truncated {
		entries = entries[:maxBrowseEntries]
	}
	// Filling in the last change is best-effort: a listing that cannot answer
	// this must still render. A failure leaves the column empty rather than
	// failing the whole directory view.
	s.attachLastCommits(ctx, repo, commit, entries)
	return TreeResult{Ref: branch, CommitSHA: commit, Path: path, Entries: entries, Truncated: truncated}, nil
}

// lastCommitRecord is one commit as reported by the history scan.
type lastCommitRecord struct {
	sha, author, committedAt, subject string
}

// attachLastCommits fills in each entry's most recent change using a single pass
// over recent history, rather than one Git call per file. Walking newest-first
// means the first commit that mentions a path is its last change, so the scan
// can stop once every entry has been answered.
//
// Merge commits are skipped. A merge does not usually change file content, so the
// branch commit that did is the more useful answer; a file touched only by merge
// conflict resolution simply reports an older commit.
func (s *Service) attachLastCommits(ctx context.Context, repo, commit string, entries []BrowseEntry) {
	if len(entries) == 0 {
		return
	}
	wanted := make(map[string]int, len(entries))
	for index := range entries {
		wanted[entries[index].Path] = index
	}
	// quotePath=false keeps non-ASCII paths byte-identical to the names the tree
	// listing produced; otherwise Git escapes them and nothing would match.
	out, err := s.browseGit(ctx, repo, maxLastCommitSize, "-c", "core.quotePath=false", "log",
		"--no-merges", "--no-renames", "-n", strconv.Itoa(maxLastCommitScan),
		"--format=%x01%H%x00%an%x00%aI%x00%s", "--name-only", commit)
	if err != nil {
		return
	}
	remaining := len(entries)
	var current *lastCommitRecord
	for _, raw := range bytes.Split(out, []byte{'\n'}) {
		line := strings.TrimSuffix(string(raw), "\r")
		if line == "" {
			continue
		}
		if line[0] == 0x01 {
			fields := strings.Split(line[1:], "\x00")
			if len(fields) != 4 || !validSHA(fields[0]) {
				return
			}
			current = &lastCommitRecord{sha: fields[0], author: fields[1], committedAt: fields[2], subject: fields[3]}
			continue
		}
		if current == nil {
			continue
		}
		// A changed path answers its own entry and every directory above it.
		candidate := line
		for {
			if index, ok := wanted[candidate]; ok {
				if entries[index].LastCommit == nil {
					entries[index].LastCommit = &BrowseCommit{SHA: current.sha, Subject: current.subject, AuthorName: current.author, CommittedAt: current.committedAt}
					remaining--
				}
			}
			slash := strings.LastIndexByte(candidate, '/')
			if slash < 0 {
				break
			}
			candidate = candidate[:slash]
		}
		if remaining == 0 {
			return
		}
	}
}

// BrowseBlob returns only bounded, UTF-8 text blobs. Binary files, Git symlinks,
// and files over maxBrowseBlobBytes are described by metadata without raw data.
func (s *Service) BrowseBlob(ctx context.Context, id, branch, path string) (BlobResult, error) {
	parts, err := browsePathParts(path, false)
	if err != nil {
		return BlobResult{}, err
	}
	repo, err := s.Path(id)
	if err != nil {
		return BlobResult{}, err
	}
	if err = s.rejectBrowseUnsafeConfig(ctx, repo); err != nil {
		return BlobResult{}, err
	}
	commit, err := s.resolveBrowseBranch(ctx, repo, branch)
	if err != nil {
		return BlobResult{}, err
	}
	target, err := s.resolveBlobRecord(ctx, repo, commit, parts)
	if err != nil {
		return BlobResult{}, err
	}
	sizeBytes, err := s.browseGit(ctx, repo, 128, "cat-file", "-s", string(target.oid))
	if err != nil {
		if ctx.Err() != nil {
			return BlobResult{}, ctx.Err()
		}
		return BlobResult{}, ErrBrowseOperation
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(sizeBytes)), 10, 64)
	if err != nil || size < 0 {
		return BlobResult{}, ErrBrowseOperation
	}
	result := BlobResult{Ref: branch, CommitSHA: commit, Path: path, BlobSHA: string(target.oid), Size: size}
	if size > maxBrowseBlobBytes {
		result.TooLarge = true
		return result, nil
	}
	content, err := s.browseGit(ctx, repo, maxBrowseBlobBytes+1, "cat-file", "blob", string(target.oid))
	if err != nil {
		if ctx.Err() != nil {
			return BlobResult{}, ctx.Err()
		}
		return BlobResult{}, ErrBrowseOperation
	}
	if int64(len(content)) != size {
		return BlobResult{}, ErrBrowseOperation
	}
	if bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content) {
		result.Binary = true
		return result, nil
	}
	result.Text = string(content)
	return result, nil
}

// resolveBlobRecord walks from a commit's tree down to one blob entry, applying
// the same rules the browser relies on: directories are crossed by object ID,
// symlinks are refused rather than followed, and only regular blob modes are
// accepted. Both the JSON browser view and the raw download use it, so the two
// can never disagree about which file a path names.
func (s *Service) resolveBlobRecord(ctx context.Context, repo, commit string, parts []string) (treeRecord, error) {
	treeOID, err := s.browseCommitTree(ctx, repo, commit)
	if err != nil {
		return treeRecord{}, err
	}
	for _, part := range parts[:len(parts)-1] {
		records, listErr := s.readTreeRecords(ctx, repo, treeOID)
		if listErr != nil {
			return treeRecord{}, listErr
		}
		found := false
		for _, rec := range records {
			if string(rec.name) == part && string(rec.kind) == "tree" && string(rec.mode) == "040000" {
				treeOID = string(rec.oid)
				found = true
				break
			}
		}
		if !found {
			return treeRecord{}, ErrBrowseNotFound
		}
	}
	records, err := s.readTreeRecords(ctx, repo, treeOID)
	if err != nil {
		return treeRecord{}, err
	}
	last := parts[len(parts)-1]
	for i := range records {
		if string(records[i].name) != last {
			continue
		}
		if string(records[i].mode) == "120000" {
			return treeRecord{}, ErrBrowseInvalid
		}
		if string(records[i].kind) != "blob" || (string(records[i].mode) != "100644" && string(records[i].mode) != "100755") {
			return treeRecord{}, ErrBrowseInvalid
		}
		return records[i], nil
	}
	return treeRecord{}, ErrBrowseNotFound
}

// resolveBrowseBranch resolves a ref to a commit. A branch is preferred and a
// tag is accepted as a fallback, so a released version can be browsed the same
// way a branch is; the fallback order means a branch always wins if a branch and
// a tag share a name.
func (s *Service) resolveBrowseBranch(ctx context.Context, repo, branch string) (string, error) {
	if !validBrowseBranch(branch) {
		return "", ErrBrowseInvalid
	}
	// The whole ref path is validated: a name that would escape its namespace or
	// be ambiguous is refused before it is ever concatenated into a ref.
	if _, err := s.browseGit(ctx, repo, 128, "check-ref-format", "refs/heads/"+branch); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", ErrBrowseInvalid
	}
	for _, prefix := range []string{"refs/heads/", "refs/tags/"} {
		out, err := s.browseGit(ctx, repo, 128, "rev-parse", "--verify", "--end-of-options", prefix+branch+"^{commit}")
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			continue
		}
		sha := strings.TrimSpace(string(out))
		if !validSHA(sha) {
			return "", ErrBrowseOperation
		}
		return sha, nil
	}
	return "", ErrBrowseNotFound
}

func (s *Service) browseCommitTree(ctx context.Context, repo, commit string) (string, error) {
	out, err := s.browseGit(ctx, repo, 128, "rev-parse", "--verify", "--end-of-options", commit+"^{tree}")
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", ErrBrowseOperation
	}
	tree := strings.TrimSpace(string(out))
	if !validSHA(tree) {
		return "", ErrBrowseOperation
	}
	return tree, nil
}

func (s *Service) readTreeRecords(ctx context.Context, repo, tree string) ([]treeRecord, error) {
	out, err := s.browseGit(ctx, repo, maxBrowseTreeOutput, "ls-tree", "-z", "--full-tree", tree)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, ErrBrowseTooLarge) {
			return nil, ErrBrowseTooLarge
		}
		return nil, ErrBrowseOperation
	}
	if len(out) == 0 {
		return []treeRecord{}, nil
	}
	if out[len(out)-1] != 0 {
		return nil, ErrBrowseOperation
	}
	records := make([]treeRecord, 0, 64)
	for _, raw := range bytes.Split(out[:len(out)-1], []byte{0}) {
		meta, name, ok := bytes.Cut(raw, []byte{'\t'})
		if !ok || len(name) == 0 {
			return nil, ErrBrowseOperation
		}
		fields := bytes.Fields(meta)
		if len(fields) != 3 || !validSHA(string(fields[2])) {
			return nil, ErrBrowseOperation
		}
		records = append(records, treeRecord{mode: append([]byte(nil), fields[0]...), kind: append([]byte(nil), fields[1]...), oid: append([]byte(nil), fields[2]...), name: append([]byte(nil), name...)})
	}
	return records, nil
}

func (s *Service) rejectBrowseUnsafeConfig(ctx context.Context, repo string) error {
	out, err := s.browseGit(ctx, repo, 64<<10, "config", "--local", "--name-only", "--list", "--no-includes")
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrBrowseOperation
	}
	for _, line := range strings.Split(strings.ToLower(string(out)), "\n") {
		key := strings.TrimSpace(line)
		if strings.HasPrefix(key, "filter.") || strings.HasPrefix(key, "include.") || strings.HasPrefix(key, "includeif.") || strings.HasPrefix(key, "merge.") && strings.HasSuffix(key, ".driver") || strings.HasPrefix(key, "diff.") && strings.HasSuffix(key, ".command") || key == "diff.external" || key == "core.fsmonitor" || key == "core.attributesfile" {
			return ErrBrowseUnsafe
		}
	}
	return nil
}

func (s *Service) browseGit(ctx context.Context, repo string, max int, args ...string) ([]byte, error) {
	all := append([]string{"-c", "core.hooksPath=" + os.DevNull, "-c", "core.attributesFile=" + os.DevNull}, args...)
	cmd := exec.CommandContext(ctx, s.git, all...)
	cmd.Dir = repo
	cmd.Env = append(gitEnv(s.git), "GIT_NO_REPLACE_OBJECTS=1", "GIT_ATTR_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	buf := &boundedBrowseBuffer{max: max}
	cmd.Stdout = buf
	cmd.Stderr = ioDiscard{}
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if buf.overflow {
			return nil, ErrBrowseTooLarge
		}
		return nil, fmt.Errorf("git browse command failed: %w", err)
	}
	if buf.overflow {
		return nil, ErrBrowseTooLarge
	}
	return buf.data, nil
}

type boundedBrowseBuffer struct {
	data     []byte
	max      int
	overflow bool
}

func (b *boundedBrowseBuffer) Write(p []byte) (int, error) {
	if len(b.data)+len(p) > b.max {
		b.overflow = true
		return 0, errors.New("Git browse output limit exceeded")
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }

func browsePathParts(path string, allowRoot bool) ([]string, error) {
	if path == "" {
		if allowRoot {
			return []string{}, nil
		}
		return nil, ErrBrowseInvalid
	}
	if len(path) > maxBrowsePathBytes || strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") || strings.ContainsAny(path, "\\\x00") || !utf8.ValidString(path) {
		return nil, ErrBrowseInvalid
	}
	parts := strings.Split(path, "/")
	if len(parts) > maxBrowsePathParts {
		return nil, ErrBrowseInvalid
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, ErrBrowseInvalid
		}
		for _, r := range part {
			if unicode.IsControl(r) {
				return nil, ErrBrowseInvalid
			}
		}
	}
	return parts, nil
}
func safeTreeName(name string) bool {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func validBrowseBranch(ref string) bool {
	if ref == "" || len(ref) > 200 || strings.HasPrefix(ref, "-") || strings.Contains(ref, "@{") || !utf8.ValidString(ref) {
		return false
	}
	for _, r := range ref {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
