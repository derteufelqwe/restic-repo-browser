// Command restic-browser serves a read-mostly web interface for restic
// repositories on local disks.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"restic-repo-browser/internal/auth"
	"restic-repo-browser/internal/cache"
	"restic-repo-browser/internal/config"
	"restic-repo-browser/internal/jobs"
	"restic-repo-browser/internal/restic"
	"restic-repo-browser/internal/secret"
	"restic-repo-browser/internal/store"
	"restic-repo-browser/internal/tasks"
	"restic-repo-browser/internal/web"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		if errors.Is(err, config.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "restic-browser:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	cfg, err := config.Parse(args, os.Stderr)
	if err != nil {
		return err
	}
	if cfg.Command == config.CmdVersion {
		fmt.Println("restic-browser", buildVersion())
		return nil
	}

	// Checked first so a wrong folder fails the start before anything is created.
	browseDir, err := config.ResolveBrowseDir(cfg.BrowseDir)
	if err != nil {
		return err
	}

	level := slog.LevelInfo
	if cfg.Debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	box, err := secret.LoadOrCreate(filepath.Join(cfg.DataDir, "secret.key"))
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, filepath.Join(cfg.DataDir, "restic-browser.db"), box)
	if err != nil {
		return err
	}
	defer st.Close()
	authSvc := auth.New(st)

	if cfg.Command == config.CmdSetPassword {
		return setPassword(ctx, authSvc)
	}
	return serve(ctx, cfg, browseDir, log, st, authSvc)
}

func serve(ctx context.Context, cfg config.Config, browseDir string, log *slog.Logger, st *store.Store, authSvc *auth.Service) error {
	bin, err := config.ResolveRestic(cfg.ResticBin)
	if err != nil {
		return err
	}
	// The restic command line client is the only backend implementation so far.
	repos := restic.New(bin, filepath.Join(cfg.DataDir, "restic-cache"), restic.WithLogging(log, cfg.LogLevel))
	version, err := repos.Version(ctx)
	if err != nil {
		return fmt.Errorf("run %s: %w", bin, err)
	}
	log.Info("starting", "restic", version, "data", cfg.DataDir, "log_level", cfg.LogLevel, "browse_dir", browseDir)

	tm := tasks.NewManager()
	refresher := cache.New(st, repos, tm, log)
	runner := jobs.New(st, repos, refresher, tm, log)
	handler, err := web.New(web.Deps{
		Store: st, Explorer: repos, Cache: refresher, Jobs: runner, Tasks: tm, Auth: authSvc, Log: log, SecureCookies: cfg.Secure, BrowseDir: browseDir,
	})
	if err != nil {
		return err
	}
	if ok, err := authSvc.HasPassword(ctx); err == nil && !ok {
		log.Warn("no login password set yet: run `restic-browser set-password` before signing in")
	}

	// Bind the port before any background work starts, so a busy port fails
	// the start without touching the data.
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Listen, err)
	}

	bg, cancelBg := context.WithCancel(ctx)
	defer cancelBg()
	done := make(chan struct{})
	go func() { refresher.Run(bg); close(done) }()
	runner.Start(bg)

	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No write timeout: downloads of large snapshots can take long.
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	log.Info("listening", "addr", ln.Addr().String())

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = srv.Shutdown(shutdownCtx)
	cancelBg()
	<-done
	runner.Wait()
	return err
}

// setPassword prompts for the new login password.
func setPassword(ctx context.Context, svc *auth.Service) error {
	read := func(prompt string) (string, error) {
		fmt.Fprint(os.Stderr, prompt)
		if term.IsTerminal(int(os.Stdin.Fd())) {
			b, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			return string(b), err
		}
		line, err := bufio.NewReader(os.Stdin).ReadString('\n') // piped input, e.g. from a script
		return strings.TrimRight(line, "\r\n"), err
	}
	pw, err := read("New password: ")
	if err != nil && pw == "" {
		return fmt.Errorf("read password: %w", err)
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		again, err := read("Repeat password: ")
		if err != nil {
			return fmt.Errorf("read password: %w", err)
		}
		if again != pw {
			return errors.New("the passwords do not match")
		}
	}
	if err := svc.SetPassword(ctx, pw); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Password saved. All sessions were signed out.")
	return nil
}

// releaseVersion is set at build time for releases (-ldflags "-X main.releaseVersion=1.2.3").
var releaseVersion string

func buildVersion() string {
	if releaseVersion != "" {
		return releaseVersion
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
