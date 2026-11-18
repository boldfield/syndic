package journal

import (
	"context"
	"database/sql"
	"embed"
	"errors"
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

// migrationWithForeignKeyDisable handles migration 004 which temporarily disables foreign key constraints.
// It uses a pinned connection to ensure PRAGMA foreign_keys and the transaction use the same connection.
func (db *DB) migrationWithForeignKeyDisable(ctx context.Context, content []byte, version int, migName string) error {
	pinned, err := db.conn.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire pinned connection for migration %s: %w", migName, err)
	}
	defer func() {
		if closeErr := pinned.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close pinned connection: %w", closeErr))
		}
	}()

	// Disable foreign keys on this connection
	if _, err := pinned.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return fmt.Errorf("disable foreign keys for migration %s: %w", migName, err)
	}
	defer func() {
		if _, reErr := pinned.ExecContext(ctx, "PRAGMA foreign_keys = ON"); reErr != nil {
			err = errors.Join(err, fmt.Errorf("re-enable foreign keys for migration %s: %w", migName, reErr))
		}
	}()

	// Begin transaction on the pinned connection
	tx, err := pinned.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction for migration %s: %w", migName, err)
	}

	// Execute migration SQL
	if _, err := tx.Exec(string(content)); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return fmt.Errorf("execute migration %s: %w; rollback also failed: %w", migName, err, rbErr)
		}
		return fmt.Errorf("execute migration %s: %w", migName, err)
	}

	// Record the applied migration
	if _, err := tx.Exec("INSERT INTO schema_version (version) VALUES (?)", version); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return fmt.Errorf("record migration %s: %w; rollback also failed: %w", migName, err, rbErr)
		}
		return fmt.Errorf("record migration %s: %w", migName, err)
	}

	// Check foreign key integrity before commit
	rows, err := tx.Query("PRAGMA foreign_key_check")
	if err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return fmt.Errorf("check foreign keys for migration %s: %w; rollback also failed: %w", migName, err, rbErr)
		}
		return fmt.Errorf("check foreign keys for migration %s: %w", migName, err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close foreign key check rows: %w", closeErr))
		}
	}()

	if rows.Next() {
		if rbErr := tx.Rollback(); rbErr != nil {
			return fmt.Errorf("migration %s violates foreign key constraints; rollback also failed: %w", migName, rbErr)
		}
		return fmt.Errorf("migration %s violates foreign key constraints", migName)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return fmt.Errorf("iterate foreign key check for migration %s: %w; rollback also failed: %w", migName, rowsErr, rbErr)
		}
		return fmt.Errorf("iterate foreign key check for migration %s: %w", migName, rowsErr)
	}

	// Commit transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", migName, err)
	}

	return nil
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

		// Migration 004 recreates the runs table with a CHECK constraint,
		// which requires temporarily disabling foreign key constraints.
		if version == 4 {
			if err := db.migrationWithForeignKeyDisable(context.Background(), content, version, mig.Name()); err != nil {
				return err
			}
		} else {
			// Standard migration path for non-004 migrations
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
	}

	return nil
}
