package journal

import (
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

		// Read and execute migration
		content, err := fs.ReadFile(migrations, "migrations/"+mig.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", mig.Name(), err)
		}

		tx, err := db.conn.Begin()
		if err != nil {
			return fmt.Errorf("begin transaction for migration %s: %w", mig.Name(), err)
		}

		if _, err := tx.Exec(string(content)); err != nil {
			if rbErr := tx.Rollback(); rbErr != nil {
				return fmt.Errorf("execute migration %s: %w; rollback also failed: %w", mig.Name(), err, rbErr)
			}
			return fmt.Errorf("execute migration %s: %w", mig.Name(), err)
		}

		// Record the applied migration
		if _, err := tx.Exec("INSERT INTO schema_version (version) VALUES (?)", version); err != nil {
			if rbErr := tx.Rollback(); rbErr != nil {
				return fmt.Errorf("record migration %s: %w; rollback also failed: %w", mig.Name(), err, rbErr)
			}
			return fmt.Errorf("record migration %s: %w", mig.Name(), err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", mig.Name(), err)
		}
	}

	return nil
}
