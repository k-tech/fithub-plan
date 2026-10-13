package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"fithub.local/fithub/internal/gitservice"
)

// GET /api/v1/repos/{team}/{repo}/raw?ref=<branch>&path=<file>
//
// Serves one file's bytes so a person can take it away. Everything is resolved
// before the first byte is written: once a status line and headers are on the
// wire an error can no longer be reported, and the client would receive a
// truncated file that looked complete.
func (s *Server) downloadRaw(w http.ResponseWriter, r *http.Request) {
	p, err := s.apiPrincipal(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load account")
		return
	}
	repo, ok := s.issueRepo(w, r, p, "read")
	if !ok {
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		ref = repo.DefaultBranch
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	info, err := s.Git.ResolveRawBlob(ctx, repo.ID, ref, r.URL.Query().Get("path"))
	if err != nil {
		writeBrowseError(w, err)
		return
	}
	w.Header().Set("Content-Type", rawContentType(info.Name))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size))
	w.Header().Set("Content-Disposition", contentDisposition(info.Name))
	// The stored bytes are served as-is. Without nosniff a browser may decide a
	// .txt file is HTML and run any script inside it on this origin.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if err := s.Git.StreamBlob(ctx, repo.ID, info.BlobSHA, w); err != nil {
		// The response has already started; the client sees a short read.
		slog.Error("raw download failed", "repo", repo.ID, "path", r.URL.Query().Get("path"), "error", err)
	}
}

// GET /api/v1/repos/{team}/{repo}/archive?ref=<branch>&format=zip|tar.gz
//
// Streams a whole revision as an archive. Git produces it server-side, so no
// working tree is created and the .git directory is never included.
func (s *Server) downloadArchive(w http.ResponseWriter, r *http.Request) {
	p, err := s.apiPrincipal(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", "Unable to load account")
		return
	}
	repo, ok := s.issueRepo(w, r, p, "read")
	if !ok {
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		ref = repo.DefaultBranch
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = gitservice.ArchiveZip
	}
	extension, ok := archiveExtension(format)
	if !ok {
		fail(w, http.StatusBadRequest, "INVALID_INPUT", "Format must be zip or tar.gz")
		return
	}
	// Archiving a large repository takes far longer than a page request, so it
	// gets its own budget rather than the default request timeout.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	info, err := s.Git.ResolveArchive(ctx, repo.ID, ref)
	if err != nil {
		writeBrowseError(w, err)
		return
	}
	name := archiveName(repo.Slug, ref) + extension
	w.Header().Set("Content-Type", archiveContentType(format))
	w.Header().Set("Content-Disposition", contentDisposition(name))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	prefix := gitservice.ArchivePrefix(repo.Slug, ref)
	if err := s.Git.StreamArchive(ctx, repo.ID, info.CommitSHA, format, prefix, w); err != nil {
		slog.Error("archive download failed", "repo", repo.ID, "ref", ref, "error", err)
	}
}

func archiveExtension(format string) (string, bool) {
	switch format {
	case gitservice.ArchiveZip:
		return ".zip", true
	case gitservice.ArchiveTarGz:
		return ".tar.gz", true
	default:
		return "", false
	}
}

func archiveContentType(format string) string {
	if format == gitservice.ArchiveZip {
		return "application/zip"
	}
	return "application/gzip"
}

// archiveName builds the download file name. Only characters that are safe in a
// header and on every common filesystem survive; anything else becomes a dash.
func archiveName(repo, ref string) string {
	sanitize := func(value string) string {
		var b strings.Builder
		for _, r := range value {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
				b.WriteRune(r)
			default:
				b.WriteRune('-')
			}
		}
		return b.String()
	}
	name := sanitize(repo)
	if name == "" {
		name = "repository"
	}
	branch := sanitize(ref)
	if branch == "" {
		branch = "archive"
	}
	return name + "-" + branch
}

// rawContentType picks a media type from the file name. The response is always
// sent as an attachment with nosniff, so a repository cannot influence how a
// browser interprets its own content.
//
// Source files are reported as plain text on purpose. Serving markup as its real
// type would let a stored .html file run script on this origin when opened; the
// extension list below covers the types people actually download and read.
func rawContentType(name string) string {
	extension := strings.ToLower(extensionOf(name))
	if textExtensions[extension] {
		return "text/plain; charset=utf-8"
	}
	detected := mime.TypeByExtension(extension)
	if index := strings.IndexByte(detected, ';'); index >= 0 {
		detected = strings.TrimSpace(detected[:index])
	}
	if strings.HasPrefix(detected, "image/") {
		return detected
	}
	if strings.HasPrefix(detected, "text/") {
		return detected + "; charset=utf-8"
	}
	return "application/octet-stream"
}

// textExtensions are the file types a person opens in the browser rather than
// saving. Everything here is served as text/plain regardless of what the
// platform's MIME database claims.
var textExtensions = map[string]bool{
	".c": true, ".cc": true, ".cfg": true, ".conf": true, ".cpp": true,
	".cs": true, ".css": true, ".csv": true, ".env": true, ".go": true,
	".h": true, ".hpp": true, ".htm": true, ".html": true, ".ini": true,
	".java": true, ".js": true, ".json": true, ".jsx": true, ".kt": true,
	".less": true, ".log": true, ".lua": true, ".md": true, ".markdown": true,
	".mod": true, ".php": true, ".pl": true, ".properties": true, ".ps1": true,
	".py": true, ".rb": true, ".rs": true, ".sass": true, ".scss": true,
	".sh": true, ".sql": true, ".sum": true, ".svg": true, ".swift": true,
	".toml": true, ".ts": true, ".tsx": true, ".txt": true, ".vue": true,
	".xml": true, ".yaml": true, ".yml": true,
}

func extensionOf(name string) string {
	index := strings.LastIndexByte(name, '.')
	if index < 0 {
		return ""
	}
	return name[index:]
}

// contentDisposition builds a download header that survives non-ASCII names.
// The quoted form is an ASCII-only fallback for old clients; the RFC 5987 form
// carries the real name. Control characters are stripped by both paths, so a
// crafted file name cannot inject a header.
func contentDisposition(filename string) string {
	if filename == "" {
		filename = "download"
	}
	fallback := make([]rune, 0, len(filename))
	for _, r := range filename {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			fallback = append(fallback, '_')
			continue
		}
		fallback = append(fallback, r)
	}
	return fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s", string(fallback), url.PathEscape(filename))
}
