package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"learninghub/internal/ai"
	"learninghub/internal/ai/anthropicapi"
	"learninghub/internal/ai/antigravity"
	"learninghub/internal/ai/procgroup"
	"learninghub/internal/config"
	"learninghub/internal/domain"
	"learninghub/internal/fsx"
	"learninghub/internal/httpserver"
	"learninghub/internal/jobs"
	"learninghub/internal/services/evaluation"
	"learninghub/internal/services/exercises"
	"learninghub/internal/services/flashcards"
	"learninghub/internal/services/lessons"
	"learninghub/internal/services/maintenance"
	"learninghub/internal/services/qa"
	"learninghub/internal/services/rebuild"
	"learninghub/internal/services/roadmaps"
	"learninghub/internal/services/topics"
	"learninghub/internal/signals"
	"learninghub/internal/store"
	"learninghub/skills"

	"github.com/gofrs/flock"
)

// Set at release time: make release (-ldflags -X).
var (
	Version   = "1.0.0"
	BuildDate = "2026-10-31"
)

// shutdownSignals contains OS signals that trigger graceful process shutdown (CR-23).
var shutdownSignals = signals.Shutdown

func main() {
	if len(os.Args) < 2 {
		serve(os.Args[1:])
		return
	}

	subcommand := os.Args[1]
	args := os.Args[2:]

	switch subcommand {
	case "serve":
		serve(args)
	case "doctor":
		runDoctor(args)
	case "migrate":
		runMigrate(args)
	case "rebuild":
		runRebuild(args)
	case "version":
		runVersion()
	default:
		// If first argument is a flag like --config, default to serve
		if strings.HasPrefix(subcommand, "-") {
			serve(os.Args[1:])
			return
		}
		fmt.Fprintf(os.Stderr, "Unknown command: %s\nUsage: hub [serve | doctor | migrate | rebuild | version]\n", subcommand)
		os.Exit(1)
	}
}

func vcsInfo() (revision string, modified bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	return revision, modified
}

func formatVersion(version, buildDate, goos, goarch, revision string, modified bool) string {
	if revision != "" {
		suffix := ""
		if modified {
			suffix = "+dirty"
		}
		return fmt.Sprintf("LearningHub CLI version %s (rev %s%s, %s, %s/%s)", version, revision, suffix, buildDate, goos, goarch)
	}
	return fmt.Sprintf("LearningHub CLI version %s (%s, %s/%s)", version, buildDate, goos, goarch)
}

func versionString() string {
	rev, mod := vcsInfo()
	return formatVersion(Version, BuildDate, runtime.GOOS, runtime.GOARCH, rev, mod)
}

func runVersion() {
	fmt.Println(versionString())
}

// runDoctor checks the environment and the AI tool (R7.3); --json prints the same as one object.
func runDoctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to config.toml (default: %APPDATA%\\LearningHub\\config.toml or ~/.config/learninghub/config.toml)")
	asJSON := fs.Bool("json", false, "Print JSON")
	_ = fs.Parse(args)

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}
	writeErr := ""
	if root, err := fsx.OpenRoot(cfg.BaseDir); err != nil {
		writeErr = err.Error()
	} else {
		// Random name + WriteNew: the probe can never overwrite a user file (CR-43).
		testFile := ".doctor_test-" + rand.Text()
		if err := root.WriteNew(testFile, []byte("ok"), 0o600); err != nil {
			writeErr = err.Error()
		} else {
			_ = root.Remove(testFile)
		}
		root.Close()
	}
	ctx, cancel := signal.NotifyContext(context.Background(), shutdownSignals...)
	defer cancel()

	kind := cfg.Adapter.Kind
	dbPath := dbFile(cfg)
	if db, err := store.OpenReadOnly(dbPath); err == nil {
		kind = httpserver.SavedAdapter(ctx, db, cfg.Adapter.Kind)
		db.Close()
	}

	adapter := newAdapter(cfg, nil, kind)
	d := httpserver.ProbeAdapter(ctx, httpserver.Settings{Adapter: adapter, Model: adapter.Model(), CompileCheck: cfg.Evaluation.CompileCheck || cfg.Evaluation.RunTests})
	ok := writeErr == "" && d.Ready()

	if *asJSON {
		res := map[string]any{
			"ok": ok, "version": Version, "os": runtime.GOOS + "/" + runtime.GOARCH, "base_dir": cfg.BaseDir, "db": dbPath,
			"base_dir_writable": writeErr == "", "base_dir_error": writeErr, "adapter": d,
		}
		if rev, mod := vcsInfo(); rev != "" {
			if mod {
				rev += "+dirty"
			}
			res["revision"] = rev
		}
		_ = json.NewEncoder(os.Stdout).Encode(res)
	} else {
		rev, mod := vcsInfo()
		revStr := ""
		if rev != "" {
			if mod {
				rev += "+dirty"
			}
			revStr = " (rev " + rev + ")"
		}
		fmt.Printf("LearningHub %s%s (%s/%s)\n", Version, revStr, runtime.GOOS, runtime.GOARCH)
		fmt.Printf("Base directory: %s\n", cfg.BaseDir)
		fmt.Printf("Database: %s\n", dbPath)
		if writeErr != "" {
			fmt.Printf("[FAIL] base directory not writable: %s\n", writeErr)
		}
		c := d.Caps
		fmt.Printf("AI tool: version=%q authenticated=%t stream-json=%t json-schema=%t models=%v\n",
			c.Version, c.Authenticated, c.HeadlessJSON, c.StructuredOutput, c.Models)
		for _, s := range append([]string{d.Err, d.Hint, d.ModelWarn}, c.Notes...) {
			if s != "" {
				fmt.Println("  " + s)
			}
		}
		fmt.Println(map[bool]string{true: "[OK] ready", false: "[FAIL] not ready"}[ok])
	}
	if !ok {
		os.Exit(1)
	}
}

func runMigrate(args []string) {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to config.toml (default: %APPDATA%\\LearningHub\\config.toml or ~/.config/learninghub/config.toml)")
	_ = fs.Parse(args)

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	root, err := fsx.OpenRoot(cfg.BaseDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening base directory: %v\n", err)
		os.Exit(1)
	}
	defer root.Close()

	if err := root.MkdirAll(".app", 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	lock := flock.New(filepath.Join(cfg.BaseDir, ".app", "hub.lock"))
	if ok, err := lock.TryLock(); err != nil || !ok {
		fmt.Fprintln(os.Stderr, "Error: LearningHub is running; stop it before migrating.")
		os.Exit(1)
	}
	defer lock.Unlock()

	dbPath := dbFile(cfg)
	fmt.Printf("Opening database and applying migrations at: %s\n", dbPath)

	db, err := store.Open(dbPath)
	if err != nil {
		_ = lock.Unlock()
		root.Close()
		fmt.Fprintf(os.Stderr, "Migration failed: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	version, err := db.UserVersion()
	if err != nil {
		db.Close()
		_ = lock.Unlock()
		root.Close()
		fmt.Fprintf(os.Stderr, "Failed to read user_version: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Database schema is up to date (user_version = %d).\n", version)
}

// runRebuild recreates the tables from the files (T6.8) into an empty database; files are only read.
func runRebuild(args []string) {
	fs := flag.NewFlagSet("rebuild", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to config.toml (default: %APPDATA%\\LearningHub\\config.toml or ~/.config/learninghub/config.toml)")
	dbPath := fs.String("db", "", "Database to create (default <base_dir>/app.db; must be new or empty)")
	_ = fs.Parse(args)

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}
	if *dbPath == "" {
		*dbPath = dbFile(cfg)
	}
	root, err := fsx.OpenRoot(cfg.BaseDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening base directory: %v\n", err)
		os.Exit(1)
	}
	// The server must not write while we rebuild (D13).
	if err := root.MkdirAll(".app", 0o755); err != nil {
		root.Close()
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	lock := flock.New(filepath.Join(cfg.BaseDir, ".app", "hub.lock"))
	if ok, err := lock.TryLock(); err != nil || !ok {
		root.Close()
		fmt.Fprintln(os.Stderr, "Error: LearningHub is running; stop it before rebuilding.")
		os.Exit(1)
	}
	defer lock.Unlock()

	db, err := store.Open(*dbPath)
	if err != nil {
		root.Close()
		fmt.Fprintf(os.Stderr, "Error opening %s: %v\n", *dbPath, err)
		os.Exit(1)
	}
	rep, err := rebuild.Run(context.Background(), db, root)
	root.Close()
	db.Close()
	if errors.Is(err, rebuild.ErrNotEmpty) {
		fmt.Fprintf(os.Stderr, "Error: %s already has records. Move it aside (or pass --db new.db) and run again.\n", *dbPath)
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Rebuild failed, nothing was recorded: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Rebuilt %s: %d roadmaps, %d topics, %d lessons, %d exercise sets, %d attempts, %d files.\n",
		*dbPath, rep.Roadmaps, rep.Topics, rep.Lessons, rep.Sets, rep.Attempts, rep.Files)
	fmt.Println("Not recoverable from files: roadmap selection rules (set to default), lesson keywords, suggestions, jobs, audit log.")
	for _, w := range rep.Warnings {
		fmt.Println("  [WARN] " + w)
	}
}

// runServe returns an error on any fatal path so main exits 1 only after the defers have run.
func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to config.toml (default: %APPDATA%\\LearningHub\\config.toml or ~/.config/learninghub/config.toml)")
	_ = fs.Parse(args)

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		return err
	}

	// Setup slog JSON logger
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	// Ensure .app internal directory exists for lock file and runtime state
	root, err := fsx.OpenRoot(cfg.BaseDir)
	if err != nil {
		slog.Error("Failed to open base directory", "path", cfg.BaseDir, "error", err)
		return err
	}
	defer root.Close()
	appDir := filepath.Join(cfg.BaseDir, ".app")
	if err := root.MkdirAll(".app", 0o755); err != nil {
		slog.Error("Failed to create .app directory", "error", err)
		return err
	}

	// Single instance lock via .app/hub.lock (D13)
	lockPath := filepath.Join(appDir, "hub.lock")
	fileLock := flock.New(lockPath)
	locked, err := fileLock.TryLock()
	if err != nil {
		slog.Error("Failed to acquire single instance lock", "path", lockPath, "error", err)
		return err
	}
	if !locked {
		fmt.Fprintf(os.Stderr, "\n[FATAL] Another instance of LearningHub is already running.\nOnly a single instance is permitted on this machine (D13).\nLock file: %s\n\n", lockPath)
		return errors.New("another instance is running")
	}
	defer func() {
		_ = fileLock.Unlock()
	}()
	// Holding the lock, no other hub uses this base dir: anything still running in .app/work outlived a crash (T12.2).
	if n := procgroup.Sweep(filepath.Join(appDir, "work")); n > 0 {
		slog.Warn("Killed processes left by a previous run", "count", n)
	}

	slog.Info("Starting LearningHub server",
		"version", Version,
		"base_dir", cfg.BaseDir,
		"listen", cfg.Listen,
	)

	// Graceful shutdown on SIGINT/SIGTERM (and SIGHUP on Unix)
	ctx, cancel := signal.NotifyContext(context.Background(), shutdownSignals...)
	defer cancel()

	// Initialize database
	dbPath := dbFile(cfg)
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		slog.Info("creating NEW database (no app.db found here)", "path", dbPath)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		slog.Error("Failed to initialize database", "path", dbPath, "error", err)
		return err
	}
	defer db.Close()

	slog.Info("Database initialized successfully", "path", dbPath)

	sm, err := httpserver.NewSecurityManager(cfg.Listen)
	if err != nil {
		slog.Error("Failed to initialize security manager", "error", err)
		return err
	}
	drafts := roadmaps.NewService(db, root)
	drafts.DraftTTL, drafts.TreeCap, drafts.MaxBytes = cfg.DraftTTL, cfg.Limits.MaxTreeTopics, int(cfg.Limits.MaxRoadmapBytes)
	sm.MaxUpload = cfg.Limits.MaxRoadmapBytes

	if n, err := skills.Install(root); err != nil {
		slog.Error("Failed to install default skills", "error", err)
		return err
	} else if n > 0 {
		slog.Info("Installed default AGENTS.md, profile and skills", "files", n)
	}
	adapter := newAdapter(cfg, root, httpserver.SavedAdapter(ctx, db, cfg.Adapter.Kind))
	runner := jobs.New(db, cfg.Jobs.MaxConcurrent)
	runner.Files = root
	runner.AutoRetry, runner.RetryDelay = cfg.Jobs.AutoRetry, cfg.Jobs.RetryDelay
	runner.Register(domain.JobKindLesson, (&lessons.Service{
		DB: db, Root: root, Adapter: adapter, Model: cfg.Adapter.Model, Language: cfg.Language,
		Timeout: cfg.Jobs.TimeoutLesson, Stall: cfg.Jobs.StallTimeout, MaxSource: cfg.Limits.MaxSourceBytes,
		MaxBytes: cfg.Limits.MaxLessonBytes,
	}).Handle)
	runner.Register(domain.JobKindExercises, (&exercises.Service{
		DB: db, Root: root, Adapter: adapter, Model: cfg.Adapter.Model, Language: cfg.Language,
		Timeout: cfg.Jobs.TimeoutExercises, Stall: cfg.Jobs.StallTimeout, Config: cfg.Exercises,
		MaxSource: cfg.Limits.MaxSourceBytes,
	}).Handle)
	runner.Register(domain.JobKindEvaluate, (&evaluation.Service{
		DB: db, Root: root, Adapter: adapter, Model: cfg.Adapter.Model, Language: cfg.Language,
		Timeout: cfg.Jobs.TimeoutEvaluateItem, JobTimeout: cfg.Jobs.TimeoutEvaluate, Stall: cfg.Jobs.StallTimeout, Parallel: cfg.Jobs.EvaluateParallelism,
		MaxAnswerBytes: cfg.Limits.MaxAnswerBytes, MaxSource: cfg.Limits.MaxSourceBytes,
		CompileCheck: cfg.Evaluation.CompileCheck, CompileTimeout: cfg.Evaluation.CompileTimeout,
		RunTests: cfg.Evaluation.RunTests, TestTimeout: cfg.Evaluation.TestTimeout,
	}).Handle)
	runner.Register(domain.JobKindQA, (&qa.Service{
		DB: db, Root: root, Adapter: adapter, Model: cfg.Adapter.Model, Language: cfg.Language,
		Timeout: cfg.Jobs.TimeoutLesson, Stall: cfg.Jobs.StallTimeout, MaxSource: cfg.Limits.MaxSourceBytes,
	}).Handle)
	runner.Register(domain.JobKindFlashcards, (&flashcards.Service{
		DB: db, Root: root, Adapter: adapter, Model: cfg.Adapter.Model, Language: cfg.Language,
		Timeout: cfg.Jobs.TimeoutLesson, Stall: cfg.Jobs.StallTimeout, MaxSource: cfg.Limits.MaxSourceBytes,
	}).Handle)
	analyzer := &roadmaps.Analyzer{
		Drafts: drafts, Adapter: adapter, Model: cfg.Adapter.Model, Timeout: cfg.Jobs.TimeoutAnalyze, Stall: cfg.Jobs.StallTimeout,
	}
	runner.Register(domain.JobKindRoadmapAnalyze, analyzer.Handle)
	runner.Register(domain.JobKindChapterDetect, analyzer.DetectChapters)
	// Maintenance at startup and hourly: expired job dirs/logs, daily DB backup, expired import drafts.
	maint := &maintenance.Service{DB: db, Root: root, LogRetention: time.Duration(cfg.LogRetentionDays) * 24 * time.Hour, KeepBackups: cfg.BackupKeep}
	srv := httpserver.NewServer(cfg.Listen, sm, drafts, topics.NewService(db, root), runner, root,
		httpserver.Settings{DB: db, Adapter: adapter, Switch: adapter, Model: cfg.Adapter.Model, SkipPermissions: cfg.Adapter.SkipPermissions, Exercises: cfg.Exercises, MaxAnswerBytes: cfg.Limits.MaxAnswerBytes, MaxViewBytes: cfg.Limits.MaxRoadmapBytes, Editor: cfg.Editor.Command, Review: cfg.Review, Maintenance: maint, CompileCheck: cfg.Evaluation.CompileCheck || cfg.Evaluation.RunTests})
	maintDone := make(chan struct{})
	go func() {
		defer close(maintDone)
		// T9.15: lessons committed before the search index existed become searchable. Runs here, not
		// before ListenAndServe, so a large backfill does not delay startup (T10.5); shutdown waits on maintDone.
		if n, err := lessons.IndexMissing(ctx, db, root); err != nil {
			slog.Error("Failed to index lesson text for search", "error", err)
		} else if n > 0 {
			slog.Info("Indexed lesson text for search", "count", n)
		}
		for t := time.NewTicker(time.Hour); ; {
			if res, err := maint.Run(ctx, time.Now(), false); err != nil {
				slog.Error("Maintenance failed", "error", err)
			} else if len(res.Removed) > 0 || res.Backup != "" {
				slog.Info("Maintenance done", "removed", len(res.Removed), "backup", res.Backup)
			}
			if n, err := drafts.PurgeExpired(ctx); err != nil {
				slog.Error("Failed to purge expired import drafts", "error", err)
			} else if n > 0 {
				slog.Info("Purged expired import drafts", "count", n)
			}
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
		}
	}()
	runnerDone := make(chan struct{})
	go func() {
		defer close(runnerDone)
		if err := runner.Run(ctx); err != nil {
			slog.Error("Job runner stopped", "error", err)
		}
	}()
	// Runs before the deferred db.Close: running jobs are cancelled (process trees killed) and recorded,
	// and the maintenance loop completes before the database connection closes (CR-I8).
	defer func() { cancel(); <-runnerDone; <-maintDone }()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	slog.Info("Listening", "url", "http://"+cfg.Listen)

	select {
	case err := <-errCh:
		slog.Error("HTTP server failed", "error", err)
		return err
	case <-ctx.Done():
	}

	slog.Info("Shutting down LearningHub gracefully...")
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("Graceful shutdown failed", "error", err)
	}
	return nil
}

func serve(args []string) {
	if runServe(args) != nil {
		os.Exit(1)
	}
}

// loadConfig is the one place base_dir is chosen (T13.2): config's base_dir, else the cwd only if it
// already holds app.db or config.toml, else config.DefaultDataDir(), so the data never follows the cwd.
func loadConfig(path string) (config.Config, error) {
	cwd, _ := os.Getwd()
	fallback, legacy, err := config.ResolveBaseDir(cwd)
	if err != nil {
		return config.Config{}, err
	}
	cfg, err := config.Load(path, fallback)
	if err != nil {
		return cfg, err
	}
	switch {
	case cfg.BaseDirSet:
	case legacy:
		slog.Warn("Data is in the current directory; set base_dir in config.toml to a fixed path, or the app will use a different folder when started elsewhere", "base_dir", cfg.BaseDir)
	default:
		if err := os.MkdirAll(cfg.BaseDir, 0o755); err != nil {
			return cfg, err
		}
	}
	if why := riskyBaseDir(cfg.BaseDir); why != "" {
		slog.Warn("base_dir looks unsafe: "+why, "base_dir", cfg.BaseDir)
	}
	return cfg, nil
}

// riskyBaseDir explains why dir is a bad home for user data: make clean/release delete dist/, and inside
// this source tree the developer's AGENTS.md is sent to the AI instead of the app's own.
func riskyBaseDir(dir string) string {
	if filepath.Base(dir) == "dist" {
		return "it is a build output folder (make clean/release delete it)"
	}
	for d := dir; ; d = filepath.Dir(d) {
		if b, err := os.ReadFile(filepath.Join(d, "go.mod")); err == nil && strings.HasPrefix(string(b), "module learninghub") {
			return "it is inside the Learning Hub source tree " + d
		}
		if filepath.Dir(d) == d {
			return ""
		}
	}
}

// dbFile is the one place the database location is defined; every subcommand uses it.
func dbFile(cfg config.Config) string { return filepath.Join(cfg.BaseDir, "app.db") }

// newAdapter builds every configured AI tool behind one switch, starting with kind.
func newAdapter(cfg config.Config, logs *fsx.Root, kind string) *ai.Switch {
	ad := cfg.Adapter
	tool := func(k string, t config.ToolConfig) ai.Adapter {
		return antigravity.New(antigravity.Config{Kind: k, Binary: t.Binary, Model: t.Model, Effort: t.Effort, ExtraArgs: t.ExtraArgs, Logs: logs})
	}
	return ai.NewSwitch(kind,
		antigravity.New(antigravity.Config{Binary: ad.Binary, Model: ad.Model, Effort: ad.Effort, Sandbox: ad.Sandbox, SkipPermissions: ad.SkipPermissions, ExtraArgs: ad.ExtraArgs, Logs: logs}),
		tool(antigravity.KindClaude, ad.Claude),
		tool(antigravity.KindCodex, ad.Codex),
		anthropicapi.New(anthropicapi.Config{Model: ad.Anthropic.Model, Effort: ad.Anthropic.Effort, MaxTokens: ad.Anthropic.MaxTokens,
			APIKeyEnv: ad.Anthropic.APIKeyEnv, Logs: logs}))
}
