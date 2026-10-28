package history

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	log "github.com/sirupsen/logrus"
	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS usage_samples (
	ts INTEGER NOT NULL,
	account_uuid TEXT NOT NULL,
	email TEXT NOT NULL,
	"window" TEXT NOT NULL,
	utilization REAL NOT NULL,
	resets_at TEXT
);
CREATE INDEX IF NOT EXISTS usage_samples_account_ts ON usage_samples (account_uuid, ts);
`

// store keeps usage samples in SQLite, forever.
type store struct {
	db *sql.DB
}

// sample is one stored row without its reset time, as /history returns it.
type sample struct {
	ts          int64
	account     string
	email       string
	window      string
	utilization float64
}

func openStore(path string) (*store, error) {
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o700); errMkdir != nil {
		return nil, fmt.Errorf("history: create database directory: %w", errMkdir)
	}
	db, errOpen := sql.Open("sqlite", path)
	if errOpen != nil {
		return nil, fmt.Errorf("history: open database: %w", errOpen)
	}
	// One connection serializes the poller and the handlers, so SQLite never reports busy.
	db.SetMaxOpenConns(1)
	if _, errExec := db.Exec(schema); errExec != nil {
		if errClose := db.Close(); errClose != nil {
			log.Errorf("history: close database: %v", errClose)
		}
		return nil, fmt.Errorf("history: create schema in %s: %w", path, errExec)
	}
	return &store{db: db}, nil
}

func (st *store) close() error {
	return st.db.Close()
}

// insert writes one account's readings taken at ts.
func (st *store) insert(ctx context.Context, ts time.Time, acc account, readings []reading) error {
	tx, errBegin := st.db.BeginTx(ctx, nil)
	if errBegin != nil {
		return errBegin
	}
	for _, r := range readings {
		var resetsAt any
		if r.resetsAt != nil {
			resetsAt = r.resetsAt.Format(time.RFC3339Nano)
		}
		_, errExec := tx.ExecContext(ctx,
			`INSERT INTO usage_samples (ts, account_uuid, email, "window", utilization, resets_at) VALUES (?, ?, ?, ?, ?, ?)`,
			ts.Unix(), acc.uuid, acc.email, r.window, r.utilization, resetsAt)
		if errExec != nil {
			if errRollback := tx.Rollback(); errRollback != nil {
				log.Errorf("history: rollback insert: %v", errRollback)
			}
			return errExec
		}
	}
	return tx.Commit()
}

// all returns every sample in time order.
func (st *store) all(ctx context.Context) ([]sample, error) {
	rows, errQuery := st.db.QueryContext(ctx,
		`SELECT ts, account_uuid, email, "window", utilization FROM usage_samples ORDER BY ts, rowid`)
	if errQuery != nil {
		return nil, errQuery
	}
	defer func() {
		if errClose := rows.Close(); errClose != nil {
			log.Errorf("history: close rows: %v", errClose)
		}
	}()
	var out []sample
	for rows.Next() {
		var smp sample
		if errScan := rows.Scan(&smp.ts, &smp.account, &smp.email, &smp.window, &smp.utilization); errScan != nil {
			return nil, errScan
		}
		out = append(out, smp)
	}
	return out, rows.Err()
}
