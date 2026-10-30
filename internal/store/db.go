package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB encapsulates the single-writer SQLite connection pool and schema migrations (D2, Section 4.1).
type DB struct {
	writer *sql.DB
	path   string
	mu     sync.Mutex
}

// Open initializes the SQLite database at dbPath, applies PRAGMAs, and runs all pending migrations.
func Open(dbPath string) (*DB, error) {
	absPath, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, fmt.Errorf("resolving db path: %w", err)
	}

	// DSN with strict settings (foreign keys, busy timeout, immediate tx lock)
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate", absPath)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite database: %w", err)
	}

	// Single writer connection pool guarantees zero SQLITE_BUSY errors on writes (Section 2.2, 4.1)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	// Explicitly enforce PRAGMAs
	pragmas := []string{
		"PRAGMA foreign_keys = ON;",
		"PRAGMA journal_mode = WAL;",
		"PRAGMA busy_timeout = 5000;",
		"PRAGMA synchronous = NORMAL;",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("executing pragma %q: %w", p, err)
		}
	}

	s := &DB{
		writer: db,
		path:   absPath,
	}

	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("running database migrations: %w", err)
	}

	return s, nil
}

// Close closes the database connection.
func (d *DB) Close() error {
	return d.writer.Close()
}

// Writer returns the underlying sql.DB instance.
func (d *DB) Writer() *sql.DB {
	return d.writer
}

// Path returns the absolute path to the database file.
func (d *DB) Path() string {
	return d.path
}

// UserVersion retrieves the current PRAGMA user_version.
func (d *DB) UserVersion() (int, error) {
	var version int
	err := d.writer.QueryRow("PRAGMA user_version;").Scan(&version)
	if err != nil {
		return 0, err
	}
	return version, nil
}

// WithTx runs fn within an immediate transaction (Section 4.1).
func (d *DB) WithTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	tx, err := d.writer.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()

	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}

	return tx.Commit()
}

// migrate applies pending embedded migrations in sequential order.
func (d *DB) migrate() error {
	currentVersion, err := d.UserVersion()
	if err != nil {
		return fmt.Errorf("reading user_version: %w", err)
	}

	// Migration 1: 0001_init.sql
	if currentVersion < 1 {
		sqlContent, err := migrationsFS.ReadFile("migrations/0001_init.sql")
		if err != nil {
			return fmt.Errorf("reading migration 0001_init.sql: %w", err)
		}

		ctx := context.Background()
		err = d.WithTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.Exec(string(sqlContent)); err != nil {
				return fmt.Errorf("executing 0001_init.sql: %w", err)
			}
			if _, err := tx.Exec("PRAGMA user_version = 1;"); err != nil {
				return fmt.Errorf("updating user_version: %w", err)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}

	return nil
}
