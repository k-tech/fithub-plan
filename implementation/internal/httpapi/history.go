package httpapi

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
)

const (
	maxHistoryBranches = 100
	maxHistoryTags     = 100
	maxHistoryFiles    = 200
	maxHistoryCommits  = 50
)

var (
	errHistoryNotFound = errors.New("history ref or commit not found")
	errHistoryInvalid  = errors.New("invalid history input")
	objectIDPattern    = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
)

type historyBranch struct {
	Name      string `json:"name"`
	SHA       string `json:"sha"`
	IsDefault bool   `json:"isDefault"`
	// The branch list shows what each branch was last changed by, which is what
	// a reader needs to tell a live branch from a stale one.
	Subject     string `json:"subject"`
	AuthorName  string `json:"authorName"`
	CommittedAt string `json:"committedAt"`
}

// historyTag describes one tag. Annotated tags point at a tag object that in
// turn points at a commit, so both object IDs are reported: the commit is what a
// link should open, and the tag object is what makes it annotated.
type historyTag struct {
	Name      string `json:"name"`
	SHA       string `json:"sha"`
	TagSHA    string `json:"tagSha,omitempty"`
	Annotated bool   `json:"annotated"`
	Subject   string `json:"subject"`
	CreatedAt string `json:"createdAt"`
}

type historyCommit struct {
	SHA         string   `json:"sha"`
	ShortSHA    string   `json:"shortSha"`
	Subject     string   `json:"subject"`
	AuthorName  string   `json:"authorName"`
	AuthorEmail string   `json:"authorEmail"`
	AuthoredAt  string   `json:"authoredAt"`
	CommittedAt string   `json:"committedAt"`
	Parents     []string `json:"parents"`
}

type historyFile struct {
	Path         string `json:"path"`
	Status       string `json:"status"`
	PreviousPath string `json:"previousPath,omitempty"`
	Additions    int    `json:"additions"`
	Deletions    int    `json:"deletions"`
	Binary       bool   `json:"binary"`
}

type historyStats struct {
	FilesChanged int `json:"filesChanged"`
	Additions    int `json:"additions"`
	Deletions    int `json:"deletions"`
}

// historyPath applies the same API principal and repository-read policy as
// repository details. Inaccessible repositories are intentionally 404.
func (s *Server) historyPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	p, err := s.apiPrincipal(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load account")
		return "", false
	}
	repo, err := s.Store.GetRepoBySlug(r.Context(), chi.URLParam(r, "team"), chi.URLParam(r, "repo"))
	if err != nil {
		fail(w, http.StatusNotFound, "NOT_FOUND", "Repository not found")
		return "", false
	}
	allowed, err := s.canRepo(r.Context(), p, repo, "read")
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to check repository access")
		return "", false
	}
	if !allowed {
		fail(w, http.StatusNotFound, "NOT_FOUND", "Repository not found")
		return "", false
	}
	path, err := s.Git.Path(repo.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "GIT_UNAVAILABLE", "Repository storage is unavailable")
		return "", false
	}
	return path, true
}

func writeHistoryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errHistoryInvalid):
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Invalid history query")
	case errors.Is(err, errHistoryNotFound):
		fail(w, http.StatusNotFound, "NOT_FOUND", "Branch or commit not found")
	case strings.Contains(err.Error(), "git output limit exceeded"):
		fail(w, http.StatusRequestEntityTooLarge, "HISTORY_TOO_LARGE", "History result exceeds the current limit")
	case errors.Is(err, context.DeadlineExceeded):
		fail(w, http.StatusGatewayTimeout, "GIT_TIMEOUT", "Git history query timed out")
	default:
		fail(w, http.StatusInternalServerError, "GIT_UNAVAILABLE", "Unable to load Git history")
	}
}

// GET /api/v1/repos/{team}/{repo}/branches
func (s *Server) branches(w http.ResponseWriter, r *http.Request) {
	p, err := s.apiPrincipal(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load account")
		return
	}
	repo, ok := s.issueRepo(w, r, p, "read")
	if !ok {
		return
	}
	path, err := s.Git.Path(repo.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "GIT_UNAVAILABLE", "Repository storage is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rows, truncated, err := readHistoryBranches(ctx, path)
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		writeHistoryError(w, err)
		return
	}
	for index := range rows {
		rows[index].IsDefault = rows[index].Name == repo.DefaultBranch
	}
	respond(w, http.StatusOK, map[string]any{"defaultBranch": repo.DefaultBranch, "branches": rows, "truncated": truncated})
}

// GET /api/v1/repos/{team}/{repo}/tags
//
// Tags are read-only here: tagging is done from a Git client. The list exists so
// a released version can be found without leaving the browser.
func (s *Server) tags(w http.ResponseWriter, r *http.Request) {
	p, err := s.apiPrincipal(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load account")
		return
	}
	repo, ok := s.issueRepo(w, r, p, "read")
	if !ok {
		return
	}
	path, err := s.Git.Path(repo.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "GIT_UNAVAILABLE", "Repository storage is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rows, truncated, err := readHistoryTags(ctx, path)
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		writeHistoryError(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"tags": rows, "truncated": truncated})
}

// GET /api/v1/repos/{team}/{repo}/commits?ref=main&limit=20&offset=0
func (s *Server) commits(w http.ResponseWriter, r *http.Request) {
	path, ok := s.historyPath(w, r)
	if !ok {
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		ref = "main"
	}
	limit, offset, err := parseHistoryPage(r)
	if err != nil {
		writeHistoryError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rows, hasMore, err := readHistoryCommits(ctx, path, ref, limit, offset)
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		writeHistoryError(w, err)
		return
	}
	nextOffset := offset + len(rows)
	respond(w, http.StatusOK, map[string]any{"ref": ref, "commits": rows, "hasMore": hasMore, "nextOffset": nextOffset})
}

// GET /api/v1/repos/{team}/{repo}/commits/{sha}
func (s *Server) commitDetail(w http.ResponseWriter, r *http.Request) {
	path, ok := s.historyPath(w, r)
	if !ok {
		return
	}
	sha := chi.URLParam(r, "sha")
	if !objectIDPattern.MatchString(sha) {
		writeHistoryError(w, errHistoryInvalid)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	commit, message, files, stats, truncated, err := readHistoryCommit(ctx, path, sha)
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		writeHistoryError(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"commit": map[string]any{
		"sha": commit.SHA, "shortSha": commit.ShortSHA, "subject": commit.Subject,
		"authorName": commit.AuthorName, "authorEmail": commit.AuthorEmail,
		"authoredAt": commit.AuthoredAt, "committedAt": commit.CommittedAt,
		"parents": commit.Parents, "message": message, "files": files,
		"stats": stats, "truncated": truncated,
	}})
}

func parseHistoryPage(r *http.Request) (int, int, error) {
	limit, offset := 20, 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if len(raw) > 3 {
			return 0, 0, errHistoryInvalid
		}
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > maxHistoryCommits {
			return 0, 0, errHistoryInvalid
		}
		limit = value
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		if len(raw) > 5 {
			return 0, 0, errHistoryInvalid
		}
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 || value > 10000 {
			return 0, 0, errHistoryInvalid
		}
		offset = value
	}
	return limit, offset, nil
}

// listBranchNames returns every branch name when the caller only needs to know
// which branches exist, such as validating a requested default branch.
func listBranchNames(ctx context.Context, path string) ([]string, bool, error) {
	rows, truncated, err := readHistoryBranches(ctx, path)
	if err != nil {
		return nil, false, err
	}
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, row.Name)
	}
	return names, truncated, nil
}

func readHistoryBranches(ctx context.Context, path string) ([]historyBranch, bool, error) {
	out, err := gitOutput(ctx, path, 512<<10, "for-each-ref", "--sort=refname", "--count=101",
		"--format=%(refname)%00%(objectname)%00%(committerdate:iso-strict)%00%(authorname)%00%(subject)", "refs/heads")
	if err != nil {
		return nil, false, err
	}
	rows := make([]historyBranch, 0)
	for _, line := range bytes.Split(bytes.TrimSuffix(out, []byte{'\n'}), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		parts := bytes.Split(line, []byte{0})
		if len(parts) != 5 || !bytes.HasPrefix(parts[0], []byte("refs/heads/")) || !objectIDPattern.Match(parts[1]) {
			return nil, false, errors.New("malformed Git branch output")
		}
		name := string(bytes.TrimPrefix(parts[0], []byte("refs/heads/")))
		rows = append(rows, historyBranch{
			Name:        name,
			SHA:         string(parts[1]),
			IsDefault:   name == "main",
			CommittedAt: string(parts[2]),
			AuthorName:  string(parts[3]),
			Subject:     string(parts[4]),
		})
	}
	truncated := len(rows) > maxHistoryBranches
	if truncated {
		rows = rows[:maxHistoryBranches]
	}
	return rows, truncated, nil
}

// readHistoryTags lists tags newest first. Annotated tags resolve to the commit
// they wrap so the link opens the tagged revision rather than the tag object.
func readHistoryTags(ctx context.Context, path string) ([]historyTag, bool, error) {
	out, err := gitOutput(ctx, path, 512<<10, "for-each-ref", "--sort=-creatordate", "--count=101",
		"--format=%(refname)%00%(objecttype)%00%(objectname)%00%(*objectname)%00%(creatordate:iso-strict)%00%(subject)", "refs/tags")
	if err != nil {
		return nil, false, err
	}
	rows := make([]historyTag, 0)
	for _, line := range bytes.Split(bytes.TrimSuffix(out, []byte{'\n'}), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		parts := bytes.Split(line, []byte{0})
		if len(parts) != 6 || !bytes.HasPrefix(parts[0], []byte("refs/tags/")) || !objectIDPattern.Match(parts[2]) {
			return nil, false, errors.New("malformed Git tag output")
		}
		name := string(bytes.TrimPrefix(parts[0], []byte("refs/tags/")))
		row := historyTag{Name: name, SHA: string(parts[2]), CreatedAt: string(parts[4]), Subject: string(parts[5])}
		// An annotated tag's own object ID is not a commit, so the dereferenced ID
		// is the one a reader can browse to.
		if string(parts[1]) == "tag" && objectIDPattern.Match(parts[3]) {
			row.Annotated = true
			row.TagSHA = row.SHA
			row.SHA = string(parts[3])
		}
		rows = append(rows, row)
	}
	truncated := len(rows) > maxHistoryTags
	if truncated {
		rows = rows[:maxHistoryTags]
	}
	return rows, truncated, nil
}

func validHistoryBranch(ctx context.Context, path, ref string) bool {
	if ref == "" || len(ref) > 200 || strings.HasPrefix(ref, "-") || strings.Contains(ref, "@{") || !utf8.ValidString(ref) {
		return false
	}
	for _, ch := range ref {
		if unicode.IsControl(ch) {
			return false
		}
	}
	_, err := gitOutput(ctx, path, 256, "check-ref-format", "--branch", ref)
	return err == nil
}

func readHistoryCommits(ctx context.Context, path, ref string, limit, offset int) ([]historyCommit, bool, error) {
	if limit < 1 || limit > maxHistoryCommits || offset < 0 || offset > 10000 || !validHistoryBranch(ctx, path, ref) {
		return nil, false, errHistoryInvalid
	}
	resolved, err := gitOutput(ctx, path, 128, "rev-parse", "--verify", "refs/heads/"+ref+"^{commit}")
	if err != nil {
		// A brand-new bare repository has no refs. Its default branch's empty
		// history is a valid result; a missing branch in a nonempty repo is 404.
		if ref == "main" {
			anyBranch, listErr := gitOutput(ctx, path, 1024, "for-each-ref", "--count=1", "--format=%(refname)", "refs/heads")
			if listErr == nil && len(anyBranch) == 0 {
				return []historyCommit{}, false, nil
			}
		}
		return nil, false, errHistoryNotFound
	}
	sha := strings.TrimSpace(string(resolved))
	if !objectIDPattern.MatchString(sha) {
		return nil, false, errors.New("malformed Git object ID")
	}
	out, err := gitOutput(ctx, path, 256<<10, "log", "--no-show-signature", "--topo-order", "--max-count="+strconv.Itoa(limit+1), "--skip="+strconv.Itoa(offset), "--format=%H%x00%P%x00%an%x00%ae%x00%aI%x00%cI%x00%s", "-z", sha)
	if err != nil {
		return nil, false, err
	}
	rows, err := parseHistoryCommits(out)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	return rows, hasMore, nil
}

func parseHistoryCommits(out []byte) ([]historyCommit, error) {
	rows := make([]historyCommit, 0)
	if len(out) == 0 {
		return rows, nil
	}
	if out[len(out)-1] != 0 {
		return nil, errors.New("malformed Git log output")
	}
	fields := bytes.Split(out[:len(out)-1], []byte{0})
	if len(fields)%7 != 0 {
		return nil, errors.New("malformed Git log fields")
	}
	for i := 0; i < len(fields); i += 7 {
		sha := string(fields[i])
		if !objectIDPattern.MatchString(sha) {
			return nil, errors.New("malformed Git commit ID")
		}
		parents := []string{}
		if len(fields[i+1]) > 0 {
			parents = strings.Fields(string(fields[i+1]))
			for _, parent := range parents {
				if !objectIDPattern.MatchString(parent) {
					return nil, errors.New("malformed Git parent ID")
				}
			}
		}
		rows = append(rows, historyCommit{
			SHA: sha, ShortSHA: sha[:7], Parents: parents,
			AuthorName: boundedHistoryText(fields[i+2], 256), AuthorEmail: boundedHistoryText(fields[i+3], 320),
			AuthoredAt: boundedHistoryText(fields[i+4], 64), CommittedAt: boundedHistoryText(fields[i+5], 64),
			Subject: boundedHistoryText(fields[i+6], 1024),
		})
	}
	return rows, nil
}

func boundedHistoryText(value []byte, maxBytes int) string {
	clean := strings.ToValidUTF8(string(value), "\ufffd")
	if len(clean) <= maxBytes {
		return clean
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(clean[cut]) {
		cut--
	}
	return clean[:cut]
}

func readHistoryCommit(ctx context.Context, path, sha string) (historyCommit, string, []historyFile, historyStats, bool, error) {
	var empty historyCommit
	var emptyStats historyStats
	if !objectIDPattern.MatchString(sha) {
		return empty, "", nil, emptyStats, false, errHistoryInvalid
	}
	typ, err := gitOutput(ctx, path, 32, "cat-file", "-t", sha)
	if err != nil || strings.TrimSpace(string(typ)) != "commit" {
		return empty, "", nil, emptyStats, false, errHistoryNotFound
	}
	meta, err := gitOutput(ctx, path, 8<<10, "log", "--no-show-signature", "--max-count=1", "--format=%H%x00%P%x00%an%x00%ae%x00%aI%x00%cI%x00%s", "-z", sha)
	if err != nil {
		return empty, "", nil, emptyStats, false, err
	}
	rows, err := parseHistoryCommits(meta)
	if err != nil || len(rows) != 1 {
		return empty, "", nil, emptyStats, false, errors.New("malformed Git commit details")
	}
	message, err := gitOutput(ctx, path, 64<<10, "show", "--no-show-signature", "-s", "--format=%B", sha)
	if err != nil {
		return empty, "", nil, emptyStats, false, err
	}
	files, stats, err := readHistoryFiles(ctx, path, sha, rows[0].Parents)
	if err != nil {
		return empty, "", nil, emptyStats, false, err
	}
	truncated := len(files) > maxHistoryFiles
	if truncated {
		files = files[:maxHistoryFiles]
	}
	return rows[0], strings.TrimSuffix(strings.ToValidUTF8(string(message), "\ufffd"), "\n"), files, stats, truncated, nil
}

func historyDiffArgs(kind, sha string, parents []string) []string {
	if len(parents) == 0 {
		return []string{"diff-tree", "--root", "--no-commit-id", "--no-ext-diff", "-r", "-M", "-z", "--" + kind, sha}
	}
	// A merge commit is displayed against its first parent, matching the
	// branch's visible net change rather than a combined-diff format.
	return []string{"diff", "--no-ext-diff", "-M", "-z", "--" + kind, parents[0], sha, "--"}
}

func readHistoryFiles(ctx context.Context, path, sha string, parents []string) ([]historyFile, historyStats, error) {
	var stats historyStats
	statusOutput, err := gitOutput(ctx, path, 1<<20, historyDiffArgs("name-status", sha, parents)...)
	if err != nil {
		return nil, stats, err
	}
	files, err := parseHistoryStatuses(statusOutput)
	if err != nil {
		return nil, stats, err
	}
	numOutput, err := gitOutput(ctx, path, 1<<20, historyDiffArgs("numstat", sha, parents)...)
	if err != nil {
		return nil, stats, err
	}
	if err := addHistoryNumstat(files, numOutput, &stats); err != nil {
		return nil, stats, err
	}
	stats.FilesChanged = len(files)
	return files, stats, nil
}

func parseHistoryStatuses(out []byte) ([]historyFile, error) {
	files := make([]historyFile, 0)
	if len(out) == 0 {
		return files, nil
	}
	if out[len(out)-1] != 0 {
		return nil, errors.New("malformed Git file status output")
	}
	fields := bytes.Split(out[:len(out)-1], []byte{0})
	for i := 0; i < len(fields); {
		if i+1 >= len(fields) || len(fields[i]) == 0 {
			return nil, errors.New("malformed Git file status fields")
		}
		rawStatus := string(fields[i])
		i++
		file := historyFile{Path: string(fields[i]), Status: historyStatusName(rawStatus)}
		i++
		if strings.HasPrefix(rawStatus, "R") || strings.HasPrefix(rawStatus, "C") {
			if i >= len(fields) {
				return nil, errors.New("malformed Git rename fields")
			}
			file.PreviousPath, file.Path = file.Path, string(fields[i])
			i++
		}
		if file.Path == "" || len(file.Path) > 4096 || len(file.PreviousPath) > 4096 {
			return nil, errors.New("invalid Git file path")
		}
		files = append(files, file)
	}
	return files, nil
}

func historyStatusName(raw string) string {
	switch raw[0] {
	case 'A':
		return "added"
	case 'M':
		return "modified"
	case 'D':
		return "deleted"
	case 'R':
		return "renamed"
	case 'C':
		return "copied"
	case 'T':
		return "typechange"
	default:
		return "unknown"
	}
}

func addHistoryNumstat(files []historyFile, out []byte, stats *historyStats) error {
	if len(out) == 0 && len(files) == 0 {
		return nil
	}
	if len(out) == 0 || out[len(out)-1] != 0 {
		return errors.New("malformed Git numstat output")
	}
	fields := bytes.Split(out[:len(out)-1], []byte{0})
	fileIndex := 0
	for i := 0; i < len(fields); {
		if fileIndex >= len(files) {
			return errors.New("Git status and numstat mismatch")
		}
		parts := bytes.SplitN(fields[i], []byte{'\t'}, 3)
		if len(parts) != 3 {
			return errors.New("malformed Git numstat fields")
		}
		i++
		path := string(parts[2])
		previous := ""
		if path == "" {
			if i+1 >= len(fields) {
				return errors.New("malformed Git numstat rename")
			}
			previous, path = string(fields[i]), string(fields[i+1])
			i += 2
		}
		file := &files[fileIndex]
		if file.Path != path || file.PreviousPath != previous {
			return errors.New("Git status and numstat paths differ")
		}
		if bytes.Equal(parts[0], []byte{'-'}) && bytes.Equal(parts[1], []byte{'-'}) {
			file.Binary = true
		} else {
			additions, addErr := strconv.Atoi(string(parts[0]))
			deletions, delErr := strconv.Atoi(string(parts[1]))
			if addErr != nil || delErr != nil || additions < 0 || deletions < 0 {
				return errors.New("malformed Git numstat count")
			}
			file.Additions, file.Deletions = additions, deletions
			stats.Additions += additions
			stats.Deletions += deletions
		}
		fileIndex++
	}
	if fileIndex != len(files) {
		return errors.New("Git status and numstat counts differ")
	}
	return nil
}
