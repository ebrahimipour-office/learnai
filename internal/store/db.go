package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"path/filepath"
	"strings"
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

// dsn formats an absolute database path into a valid SQLite URI DSN with required settings.
// It escapes '%', '#', and '?' in the path (CR-14) so SQLite's URI parser does not treat them
// as percent-decoding, fragment delimiters, or query parameter starts.
func dsn(absPath string) string {
	escaped := strings.NewReplacer("%", "%25", "#", "%23", "?", "%3f").Replace(filepath.ToSlash(absPath))
	return fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate", escaped)
}

// Open initializes the SQLite database at dbPath, applies PRAGMAs, and runs all pending migrations.
func Open(dbPath string) (*DB, error) {
	absPath, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, fmt.Errorf("resolving db path: %w", err)
	}

	db, err := sql.Open("sqlite", dsn(absPath))
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

// migrations run in order; file i sets user_version i+1.
var migrations = []string{"0001_init.sql", "0002_eval_guards.sql", "0003_chapters.sql", "0004_suggestion_keys.sql", "0005_topic_tree.sql"}

// migrate applies all pending embedded migrations.
func (d *DB) migrate() error { return d.migrateTo(len(migrations)) }

// migrateTo applies pending migrations up to version target. Each file runs in its own transaction on
// one dedicated connection with foreign_keys OFF (the PRAGMA is a no-op inside a transaction), so a
// file may rebuild a referenced table (design §12.6); foreign_key_check must be empty before commit.
func (d *DB) migrateTo(target int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	ctx := context.Background()
	conn, err := d.writer.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	var current int
	if err := conn.QueryRowContext(ctx, "PRAGMA user_version;").Scan(&current); err != nil {
		return fmt.Errorf("reading user_version: %w", err)
	}
	if current >= target {
		return nil
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF;"); err != nil {
		return err
	}
	// Restored even on failure: the pool reuses this connection for every later query.
	defer conn.ExecContext(ctx, "PRAGMA foreign_keys = ON;")

	for v := current + 1; v <= target; v++ {
		name := migrations[v-1]
		sqlContent, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("reading migration %s: %w", name, err)
		}
		if err := applyMigration(ctx, conn, name, string(sqlContent), v); err != nil {
			return err
		}
	}
	return nil
}

func applyMigration(ctx context.Context, conn *sql.Conn, name, content string, v int) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning %s: %w", name, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, content); err != nil {
		return fmt.Errorf("executing %s: %w", name, err)
	}
	rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check;")
	if err != nil {
		return fmt.Errorf("checking foreign keys after %s: %w", name, err)
	}
	broken := rows.Next()
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("checking foreign keys after %s: %w", name, err)
	}
	rows.Close()
	if broken {
		return fmt.Errorf("executing %s: foreign_key_check reports broken references", name)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d;", v)); err != nil {
		return fmt.Errorf("updating user_version: %w", err)
	}
	return tx.Commit()
}
