package main

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS links (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		short_code   TEXT    NOT NULL UNIQUE,
		original_url TEXT    NOT NULL,
		created_at   TEXT    NOT NULL DEFAULT (datetime('now'))
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_links_short_code ON links(short_code)`,
	`CREATE TABLE IF NOT EXISTS analytics (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		link_id    INTEGER NOT NULL REFERENCES links(id) ON DELETE CASCADE,
		clicked_at TEXT    NOT NULL DEFAULT (datetime('now')),
		ip_address TEXT,
		country    TEXT
	)`,
	`CREATE INDEX IF NOT EXISTS idx_analytics_link_id ON analytics(link_id)`,
	`CREATE INDEX IF NOT EXISTS idx_analytics_link_clicked ON analytics(link_id, clicked_at)`,
}

type Database struct {
	db *sql.DB
}

func NewDatabase(path string) (*Database, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	conn.SetMaxOpenConns(1)

	d := &Database{db: conn}
	if err := d.configure(); err != nil {
		conn.Close()
		return nil, err
	}
	return d, nil
}

func (d *Database) configure() error {
	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA foreign_keys = ON",
		"PRAGMA synchronous = NORMAL",
	} {
		if _, err := d.db.Exec(pragma); err != nil {
			return fmt.Errorf("apply pragma %q: %w", pragma, err)
		}
	}
	return nil
}

func (d *Database) InitSchema(ctx context.Context) error {
	for _, stmt := range schemaStatements {
		if _, err := d.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("init schema: %w", err)
		}
	}
	return nil
}

func (d *Database) Close() error {
	return d.db.Close()
}

func (d *Database) FetchOne(ctx context.Context, query string, args ...any) *sql.Row {
	return d.db.QueryRowContext(ctx, query, args...)
}

func (d *Database) FetchAll(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.db.QueryContext(ctx, query, args...)
}

func (d *Database) Execute(ctx context.Context, query string, args ...any) (int64, error) {
	res, err := d.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (d *Database) Ping(ctx context.Context) bool {
	return d.db.PingContext(ctx) == nil
}
