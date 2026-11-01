package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"learninghub/internal/ai/antigravity"
	"learninghub/internal/config"
	"learninghub/internal/domain"
	"learninghub/internal/fsx"
	"learninghub/internal/httpserver"
	"learninghub/internal/jobs"
	"learninghub/internal/services/lessons"
	"learninghub/internal/services/roadmaps"
	"learninghub/internal/services/topics"
	"learninghub/internal/store"
	"learninghub/skills"

	"github.com/gofrs/flock"
)

const (
	Version   = "1.0.0"
	BuildDate = "2026-10-31"
)

func main() {
	if len(os.Args) < 2 {
		runServe(os.Args[1:])
		return
	}

	subcommand := os.Args[1]
	args := os.Args[2:]

	switch subcommand {
	case "serve":
		runServe(args)
	case "doctor":
		runDoctor(args)
	case "migrate":
		runMigrate(args)
	case "version":
		runVersion()
	default:
		// If first argument is a flag like --config, default to serve
		if strings.HasPrefix(subcommand, "-") {
			runServe(os.Args[1:])
			return
		}
		fmt.Fprintf(os.Stderr, "Unknown command: %s\nUsage: hub [serve | doctor | migrate | version]\n", subcommand)
		os.Exit(1)
	}
}

func runVersion() {
	fmt.Printf("LearningHub CLI version %s (%s, %s/%s)\n", Version, BuildDate, runtime.GOOS, runtime.GOARCH)
}

func runDoctor(args []string) {
	fmt.Println("=== LearningHub System Doctor (T1.1) ===")
	fmt.Printf("OS: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Printf("Go Runtime: %s\n", runtime.Version())

	// Check Antigravity CLI
	agyPath, err := exec.LookPath("agy")
	if err != nil {
		fmt.Printf("[FAIL] Antigravity CLI (agy): Not found in PATH. Please install or configure binary path.\n")
	} else {
		out, err := exec.Command(agyPath, "--version").CombinedOutput()
		if err != nil {
			fmt.Printf("[WARN] Antigravity CLI found at %s, but failed to run --version: %v\n", agyPath, err)
		} else {
			fmt.Printf("[OK] Antigravity CLI found: %s (%s)\n", agyPath, strings.TrimSpace(string(out)))
		}
	}

	// Check current working directory
	cwd, _ := os.Getwd()
	fmt.Printf("[OK] Current Base Directory: %s\n", cwd)

	// Check writable permissions
	testFile := filepath.Join(cwd, ".doctor_test")
	if err := os.WriteFile(testFile, []byte("ok"), 0644); err != nil {
		fmt.Printf("[FAIL] Directory write test failed: %v\n", err)
	} else {
		_ = os.Remove(testFile)
		fmt.Printf("[OK] Directory write permission verified\n")
	}

	fmt.Println("Doctor checks completed successfully.")
}

func runMigrate(args []string) {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to config.toml")
	_ = fs.Parse(args)

	cwd, _ := os.Getwd()
	cfg, err := config.Load(*configPath, cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	dbPath := filepath.Join(cfg.BaseDir, "app.db")
	fmt.Printf("Opening database and applying migrations at: %s\n", dbPath)

	db, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Migration failed: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	version, err := db.UserVersion()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read user_version: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Database schema is up to date (user_version = %d).\n", version)
}

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to config.toml")
	_ = fs.Parse(args)

	cwd, _ := os.Getwd()
	cfg, err := config.Load(*configPath, cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}

	// Setup slog JSON logger
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	// Ensure .app internal directory exists for lock file and runtime state
	appDir := filepath.Join(cfg.BaseDir, ".app")
	if err := os.MkdirAll(appDir, 0755); err != nil {
		slog.Error("Failed to create .app directory", "error", err)
		os.Exit(1)
	}

	// Single instance lock via .app/hub.lock (D13)
	lockPath := filepath.Join(appDir, "hub.lock")
	fileLock := flock.New(lockPath)
	locked, err := fileLock.TryLock()
	if err != nil {
		slog.Error("Failed to acquire single instance lock", "path", lockPath, "error", err)
		os.Exit(1)
	}
	if !locked {
		fmt.Fprintf(os.Stderr, "\n[FATAL] Another instance of LearningHub is already running.\nOnly a single instance is permitted on this machine (D13).\nLock file: %s\n\n", lockPath)
		os.Exit(1)
	}
	defer func() {
		_ = fileLock.Unlock()
	}()

	slog.Info("Starting LearningHub server",
		"version", Version,
		"base_dir", cfg.BaseDir,
		"listen", cfg.Listen,
	)

	// Setup graceful shutdown on SIGINT/SIGTERM
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Initialize database
	dbPath := filepath.Join(cfg.BaseDir, "app.db")
	db, err := store.Open(dbPath)
	if err != nil {
		slog.Error("Failed to initialize database", "path", dbPath, "error", err)
		os.Exit(1)
	}
	defer db.Close()

	slog.Info("Database initialized successfully", "path", dbPath)

	sm, err := httpserver.NewSecurityManager(cfg.Listen)
	if err != nil {
		slog.Error("Failed to initialize security manager", "error", err)
		return
	}
	root, err := fsx.OpenRoot(cfg.BaseDir)
	if err != nil {
		slog.Error("Failed to open base directory", "path", cfg.BaseDir, "error", err)
		return
	}
	defer root.Close()
	drafts := roadmaps.NewService(db, root)
	if n, err := drafts.PurgeExpired(ctx); err != nil {
		slog.Error("Failed to purge expired import drafts", "error", err)
	} else if n > 0 {
		slog.Info("Purged expired import drafts", "count", n)
	}
	// shortcut: expired drafts are purged at startup and on each upload, not on a timer; T6.5 maintenance can add one.

	if n, err := skills.Install(root); err != nil {
		slog.Error("Failed to install default skills", "error", err)
		return
	} else if n > 0 {
		slog.Info("Installed default AGENTS.md, profile and skills", "files", n)
	}
	adapter := antigravity.New(antigravity.Config{
		Binary: cfg.Adapter.Binary, Model: cfg.Adapter.Model, Effort: cfg.Adapter.Effort,
		Sandbox: cfg.Adapter.Sandbox, ExtraArgs: cfg.Adapter.ExtraArgs, Logs: root,
	})
	runner := jobs.New(db, cfg.Jobs.MaxConcurrent)
	runner.Files = root
	runner.Register(domain.JobKindLesson, (&lessons.Service{
		DB: db, Root: root, Adapter: adapter, Model: cfg.Adapter.Model, Language: cfg.Language,
		Timeout: cfg.Jobs.TimeoutLesson, Stall: cfg.Jobs.StallTimeout,
	}).Handle)
	srv := httpserver.NewServer(cfg.Listen, sm, drafts, topics.NewService(db), runner, root)
	runnerDone := make(chan struct{})
	go func() {
		defer close(runnerDone)
		if err := runner.Run(ctx); err != nil {
			slog.Error("Job runner stopped", "error", err)
		}
	}()
	// Runs before the deferred db.Close: running jobs are cancelled (process trees killed) and recorded.
	defer func() { cancel(); <-runnerDone }()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	slog.Info("Listening", "url", "http://"+cfg.Listen)

	select {
	case err := <-errCh:
		slog.Error("HTTP server failed", "error", err)
		return // return (not os.Exit) so deferred DB close and unlock run
	case <-ctx.Done():
	}

	slog.Info("Shutting down LearningHub gracefully...")
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("Graceful shutdown failed", "error", err)
	}
}
