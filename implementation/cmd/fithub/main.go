package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"fithub.local/fithub/internal/gitservice"
	"fithub.local/fithub/internal/httpapi"
	"fithub.local/fithub/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		slog.Error("FitHub stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	devMode := os.Getenv("FITHUB_DEV_MODE") == "1"
	address := getenv("FITHUB_LISTEN", "127.0.0.1:8080")
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("FITHUB_LISTEN must be a host:port address")
	}
	if devMode && !isLoopbackHost(host) {
		return errors.New("development mode must bind a loopback address")
	}
	publicURL := getenv("FITHUB_PUBLIC_URL", "http://127.0.0.1:8080")
	if err := validatePublicURL(publicURL, devMode); err != nil {
		return err
	}
	appURL := getenv("FITHUB_APP_URL", publicURL)
	if err := validateAppURL(appURL, publicURL, devMode); err != nil {
		return err
	}
	sshBaseURL := os.Getenv("FITHUB_SSH_BASE_URL")
	if sshBaseURL != "" {
		if err := validateSSHBaseURL(sshBaseURL); err != nil {
			return err
		}
	}
	clientID := os.Getenv("FITHUB_GOOGLE_CLIENT_ID")
	clientSecret := os.Getenv("FITHUB_GOOGLE_CLIENT_SECRET")
	hostedDomain := os.Getenv("FITHUB_GOOGLE_HOSTED_DOMAIN")
	if !devMode && (clientID == "" || clientSecret == "" || hostedDomain == "") {
		return errors.New("production requires FITHUB_GOOGLE_CLIENT_ID, FITHUB_GOOGLE_CLIENT_SECRET, and FITHUB_GOOGLE_HOSTED_DOMAIN")
	}
	if (clientID != "" || clientSecret != "" || hostedDomain != "") && (clientID == "" || clientSecret == "" || hostedDomain == "") {
		return errors.New("Google OIDC configuration requires client ID, secret, and hosted domain together")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()
	connection := os.Getenv("FITHUB_DATABASE_URL")
	if connection == "" {
		return errors.New("FITHUB_DATABASE_URL is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := pgxpool.New(ctx, connection)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return err
	}
	data := store.New(pool)
	if err := data.Migrate(ctx); err != nil {
		return err
	}
	if err := data.PruneExpiredAuth(ctx); err != nil {
		return err
	}
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pruneCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				if err := data.PruneExpiredAuth(pruneCtx); err != nil {
					slog.Warn("unable to prune expired authentication records", "error", err)
				}
				cancel()
			}
		}
	}()
	root, err := filepath.Abs(getenv("FITHUB_GIT_ROOT", "./data/repos"))
	if err != nil {
		return err
	}
	git, err := gitservice.New(root)
	if err != nil {
		return err
	}
	// Wire the database-backed receive hook. Without it a push could not
	// re-check the pushing account, so production must refuse to start rather
	// than run with the old bypassable shell hook.
	hookConfigPath := os.Getenv("FITHUB_HOOK_CONFIG")
	hookBinary := os.Getenv("FITHUB_HOOK_BINARY")
	if hookConfigPath != "" || hookBinary != "" {
		if hookConfigPath == "" || hookBinary == "" {
			return errors.New("FITHUB_HOOK_CONFIG and FITHUB_HOOK_BINARY must be set together")
		}
		git.SetHookInstallation(gitservice.HookInstallation{ConfigPath: hookConfigPath, ExecutablePath: hookBinary})
	} else if !devMode {
		return errors.New("production requires FITHUB_HOOK_CONFIG and FITHUB_HOOK_BINARY so current Git write permissions are enforced by the database")
	}
	var google *httpapi.GoogleOIDC
	if clientID != "" {
		callbackURL := strings.TrimRight(publicURL, "/") + "/api/v1/auth/google/callback"
		discoveryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		google, err = httpapi.NewGoogleOIDC(discoveryCtx, clientID, clientSecret, callbackURL, hostedDomain)
		cancel()
		if err != nil {
			return err
		}
	}
	api := httpapi.New(data, git, httpapi.Config{
		DevMode: devMode, DevAdminEmail: getenv("FITHUB_DEV_ADMIN_EMAIL", "admin@fithub.local"),
		PublicURL: publicURL, AppURL: appURL, Google: google,
		SSHBaseURL: sshBaseURL,
	})
	// Resolve merges that updated Git but lost their database reply before the
	// previous process stopped. Do this before accepting any new requests.
	if err := api.ReconcilePendingMerges(ctx); err != nil {
		return err
	}
	// Repositories created before the database-backed receive hook existed would
	// still run an older or missing hook. Reconcile every repository so no push
	// path skips the current policy.
	if err := reconcileReceiveHooks(ctx, data, git); err != nil {
		return err
	}
	// The CI trigger worker is the only component that reads workflow files and
	// turns queued events into runs. It runs in this process because it needs the
	// repository storage and the database together.
	workerCtx, stopWorker := context.WithCancel(ctx)
	defer stopWorker()
	go (&httpapi.CITriggerWorker{Store: data, Git: git}).Run(workerCtx, ciWorkerInterval)
	// A lease whose runner stopped responding would otherwise hold its job until
	// the queue drained; the reaper requeues those jobs.
	go reapExpiredLeases(workerCtx, data)
	server := &http.Server{Handler: api.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 10 * time.Minute, MaxHeaderBytes: 1 << 20}
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	slog.Info("FitHub server ready", "listen", listener.Addr().String(), "gitRoot", root, "devMode", devMode, "googleAuth", google != nil, "branchHook", hookConfigPath != "")
	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}

func validatePublicURL(raw string, devMode bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("FITHUB_PUBLIC_URL must be a root URL without credentials, query, or fragment")
	}
	if devMode {
		if u.Scheme != "http" && u.Scheme != "https" {
			return errors.New("development FITHUB_PUBLIC_URL must use HTTP or HTTPS")
		}
		if !isLoopbackHost(u.Hostname()) {
			return errors.New("development FITHUB_PUBLIC_URL must use a loopback host")
		}
	} else if u.Scheme != "https" {
		return errors.New("production FITHUB_PUBLIC_URL must use HTTPS")
	}
	return nil
}

func validateAppURL(raw, publicURL string, devMode bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("FITHUB_APP_URL must be an absolute URL without credentials, query, or fragment")
	}
	if devMode {
		public, _ := url.Parse(publicURL)
		if (u.Scheme != "http" && u.Scheme != "https") || !isLoopbackHost(u.Hostname()) || u.Hostname() != public.Hostname() {
			return errors.New("development FITHUB_APP_URL must use the same loopback hostname as FITHUB_PUBLIC_URL")
		}
		return nil
	}
	public, _ := url.Parse(publicURL)
	if u.Scheme != "https" || u.Host != public.Host {
		return errors.New("production FITHUB_APP_URL must share the HTTPS origin of FITHUB_PUBLIC_URL")
	}
	return nil
}

func validateSSHBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "ssh" || u.Hostname() == "" || u.User == nil || u.User.Username() == "" || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("FITHUB_SSH_BASE_URL must be ssh://user@host[:port] without a path")
	}
	if _, hasPassword := u.User.Password(); hasPassword {
		return errors.New("FITHUB_SSH_BASE_URL must not include a password")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return errors.New("FITHUB_SSH_BASE_URL has an invalid port")
		}
	}
	return nil
}

// ciWorkerInterval is how often the trigger worker looks for queued events. It
// is short enough that a push starts a run promptly and long enough not to poll
// the database continuously.
const ciWorkerInterval = 3 * time.Second

// reapExpiredLeases periodically requeues jobs whose runner stopped responding.
// Failing to reap is not fatal: the next tick tries again.
func reapExpiredLeases(ctx context.Context, data *store.Store) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := data.ReapExpiredLeases(ctx); err != nil {
				slog.Warn("cannot reap expired CI leases", "error", err)
			}
		}
	}
}

// reconcileReceiveHooks reinstalls the receive hook on every existing
// repository. It runs before the server accepts requests so an old repository
// cannot accept pushes with an outdated permission-checking hook. A failure
// stops startup rather than serving without current write-access checks.
func reconcileReceiveHooks(ctx context.Context, data *store.Store, git *gitservice.Service) error {
	ids, err := data.ListRepositoryIDs(ctx)
	if err != nil {
		return fmt.Errorf("list repositories for hook reconciliation: %w", err)
	}
	for _, id := range ids {
		if err := git.InstallHook(id); err != nil {
			return fmt.Errorf("install receive hook for %s: %w", id, err)
		}
	}
	return nil
}

func getenv(name, fallback string) string {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	return value
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
