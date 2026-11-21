package journal

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// DB wraps a SQLite database connection with schema management.
type DB struct {
	conn *sql.DB
}

// Open opens or creates a SQLite database at the given path, applies pending
// migrations, and returns a handle. It enables WAL mode and foreign key
// constraints. The caller must call Close on the returned DB.
func Open(path string) (*DB, error) {
	// Use DSN to enable WAL mode and foreign keys on all connections
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	db := &DB{conn: sqlDB}

	// Apply any pending migrations
	if err := db.migrate(); err != nil {
		if closeErr := sqlDB.Close(); closeErr != nil {
			return nil, fmt.Errorf("migration failed: %w; close also failed: %w", err, closeErr)
		}
		return nil, err
	}

	return db, nil
}

// Close closes the database connection.
func (db *DB) Close() error {
	return db.conn.Close()
}

// Conn returns the underlying *sql.DB for testing.
func (db *DB) Conn() *sql.DB {
	return db.conn
}

// migrate applies all pending schema migrations.
func (db *DB) migrate() error {
	// Create schema_version table if it doesn't exist
	if _, err := db.conn.Exec(`
		CREATE TABLE IF NOT EXISTS schema_version (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return fmt.Errorf("create schema_version table: %w", err)
	}

	// Get current schema version
	var currentVersion int
	err := db.conn.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&currentVersion)
	if err != nil {
		return fmt.Errorf("query schema version: %w", err)
	}

	// Get all migrations in order
	migrationFiles, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}

	// Pin one connection for all migrations: PRAGMA foreign_keys is
	// per-connection and a no-op inside a transaction, so it must be set on
	// the same connection, outside the transaction, that applies the DDL.
	ctx := context.Background()
	conn, err := db.conn.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// Apply migrations sequentially
	for _, mig := range migrationFiles {
		if mig.IsDir() {
			continue
		}

		// Parse migration number from filename (e.g., "001_init.sql" -> 1)
		var version int
		if _, err := fmt.Sscanf(mig.Name(), "%d_", &version); err != nil {
			continue // Skip files that don't match naming convention
		}

		if version <= currentVersion {
			continue // Already applied
		}

		content, err := fs.ReadFile(migrations, "migrations/"+mig.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", mig.Name(), err)
		}

		if err := applyMigration(ctx, conn, mig.Name(), version, string(content)); err != nil {
			return err
		}
	}

	return nil
}

// applyMigration runs one migration and records its version in a single
// transaction on conn. Foreign keys are off while it runs so tables can be
// rebuilt; foreign_key_check re-validates before commit and enforcement is
// restored afterwards.
func applyMigration(ctx context.Context, conn *sql.Conn, name string, version int, content string) (err error) {
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return fmt.Errorf("disable foreign keys for migration %s: %w", name, err)
	}
	defer func() {
		if _, ferr := conn.ExecContext(ctx, "PRAGMA foreign_keys = ON"); ferr != nil && err == nil {
			err = fmt.Errorf("restore foreign keys after migration %s: %w", name, ferr)
		}
	}()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction for migration %s: %w", name, err)
	}
	fail := func(err error) error {
		if rbErr := tx.Rollback(); rbErr != nil {
			return fmt.Errorf("%w; rollback also failed: %w", err, rbErr)
		}
		return err
	}

	if _, err := tx.ExecContext(ctx, content); err != nil {
		return fail(fmt.Errorf("execute migration %s: %w", name, err))
	}

	rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fail(fmt.Errorf("foreign key check after migration %s: %w", name, err))
	}
	violated := rows.Next()
	if closeErr := rows.Close(); closeErr != nil {
		return fail(fmt.Errorf("foreign key check after migration %s: %w", name, closeErr))
	}
	if violated {
		return fail(fmt.Errorf("migration %s left foreign key violations", name))
	}

	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_version (version) VALUES (?)", version); err != nil {
		return fail(fmt.Errorf("record migration %s: %w", name, err))
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", name, err)
	}
	return nil
}
