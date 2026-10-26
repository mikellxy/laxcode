package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

type ledger struct {
	db           *sql.DB
	id           uuid.UUID
	limit, chunk uint64
}
type epoch struct {
	ID, User                  uint64
	UUID, Day, Status         string
	Granted, RX, TX, Deadline uint64
}
type usageResponse struct {
	UserID      string `json:"user_id"`
	Day         string `json:"day"`
	Limit       uint64 `json:"limit_bytes"`
	Observed    uint64 `json:"observed_used_bytes"`
	Confirmed   uint64 `json:"confirmed_used_bytes"`
	Held        uint64 `json:"held_bytes"`
	Allocatable uint64 `json:"allocatable_bytes"`
	Uncertain   uint64 `json:"uncertain_bytes"`
	Exhausted   bool   `json:"exhausted"`
	Stale       bool   `json:"stale"`
	UpdatedAt   string `json:"updated_at"`
	ResetAt     string `json:"reset_at"`
}

func openLedger(path string, limit, chunk uint64) (*ledger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	l := &ledger{db: db, limit: limit, chunk: chunk}
	ok := false
	defer func() {
		if !ok {
			db.Close()
		}
	}()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`
CREATE TABLE IF NOT EXISTS metadata (
 id INTEGER PRIMARY KEY CHECK(id=1), db_id TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS users (id INTEGER PRIMARY KEY AUTOINCREMENT, uuid TEXT NOT NULL UNIQUE);
CREATE TABLE IF NOT EXISTS user_days (
 user_id INTEGER NOT NULL REFERENCES users(id), day TEXT NOT NULL,
 limit_bytes INTEGER NOT NULL CHECK(limit_bytes>0), PRIMARY KEY(user_id,day)
);
CREATE TABLE IF NOT EXISTS epochs (
 id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, day TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('active','closed')),
 granted INTEGER NOT NULL DEFAULT 0 CHECK(granted>=0),
 rx INTEGER NOT NULL DEFAULT 0 CHECK(rx>=0), tx INTEGER NOT NULL DEFAULT 0 CHECK(tx>=0),
 deadline INTEGER NOT NULL,
 updated_at TEXT NOT NULL,
 FOREIGN KEY(user_id,day) REFERENCES user_days(user_id,day),
 CHECK(rx+tx<=granted)
);
CREATE UNIQUE INDEX IF NOT EXISTS active_epoch ON epochs(user_id,day) WHERE status='active';
PRAGMA user_version=3;`)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	if _, err = db.Exec("INSERT OR IGNORE INTO metadata(id,db_id) VALUES(1,?)", uuid.NewString()); err != nil {
		return nil, err
	}
	var id string
	if err = db.QueryRow("SELECT db_id FROM metadata WHERE id=1").Scan(&id); err != nil {
		return nil, err
	}
	l.id, err = uuid.Parse(id)
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		return nil, err
	}
	ok = true
	return l, nil
}

const epochColumns = "e.id,e.user_id,u.uuid,e.day,e.status,e.granted,e.rx,e.tx,e.deadline"

func scanEpoch(row interface{ Scan(...any) error }) (e epoch, err error) {
	err = row.Scan(&e.ID, &e.User, &e.UUID, &e.Day, &e.Status, &e.Granted, &e.RX, &e.TX, &e.Deadline)
	return
}
func (l *ledger) active(ctx context.Context) ([]epoch, error) {
	rows, err := l.db.QueryContext(ctx, "SELECT "+epochColumns+" FROM epochs e JOIN users u ON u.id=e.user_id WHERE e.status='active'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []epoch
	for rows.Next() {
		e, err := scanEpoch(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

func (l *ledger) userID(ctx context.Context, id string) (uint64, error) {
	if _, err := l.db.ExecContext(ctx, "INSERT OR IGNORE INTO users(uuid) VALUES(?)", id); err != nil {
		return 0, err
	}
	var user uint64
	err := l.db.QueryRowContext(ctx, "SELECT id FROM users WHERE uuid=?", id).Scan(&user)
	return user, err
}

func (l *ledger) latestActive(ctx context.Context, user uint64) (epoch, error) {
	return scanEpoch(l.db.QueryRowContext(ctx, "SELECT "+epochColumns+" FROM epochs e JOIN users u ON u.id=e.user_id WHERE e.user_id=? AND e.status='active' ORDER BY e.day DESC,e.id DESC LIMIT 1", user))
}

func (l *ledger) ensure(ctx context.Context, user uint64, day string, deadline uint64) (epoch, error) {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return epoch{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO user_days(user_id,day,limit_bytes) VALUES(?,?,?)", user, day, l.limit); err != nil {
		return epoch{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO epochs(user_id,day,status,deadline,updated_at)
SELECT ?,?,'active',?,? WHERE NOT EXISTS(SELECT 1 FROM epochs WHERE user_id=? AND day=? AND status='active')`,
		user, day, deadline, time.Now().UTC().Format(time.RFC3339Nano), user, day)
	if err != nil {
		return epoch{}, err
	}
	e, err := scanEpoch(tx.QueryRowContext(ctx, "SELECT "+epochColumns+" FROM epochs e JOIN users u ON u.id=e.user_id WHERE e.user_id=? AND e.day=? AND e.status='active'", user, day))
	if err != nil {
		return epoch{}, err
	}
	return e, tx.Commit()
}

// Persist cumulative authorization before exposing it to the kernel. Usage is a
// locked cumulative snapshot, never a delta that can be applied twice.
func (l *ledger) checkpoint(ctx context.Context, id uint64, u usage, refill bool) (epoch, error) {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return epoch{}, err
	}
	defer tx.Rollback()
	e, err := scanEpoch(tx.QueryRowContext(ctx, "SELECT "+epochColumns+" FROM epochs e JOIN users u ON u.id=e.user_id WHERE e.id=?", id))
	if err != nil {
		return epoch{}, err
	}
	if e.Status != "active" || u.RX < e.RX || u.TX < e.TX ||
		u.RX > e.Granted || u.TX > e.Granted-u.RX {
		return epoch{}, errors.New("usage snapshot violates epoch invariants")
	}
	var held, limit uint64
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(granted),0) FROM epochs WHERE user_id=? AND day=?", e.User, e.Day).Scan(&held); err != nil {
		return epoch{}, err
	}
	if err = tx.QueryRowContext(ctx, "SELECT limit_bytes FROM user_days WHERE user_id=? AND day=?", e.User, e.Day).Scan(&limit); err != nil {
		return epoch{}, err
	}
	if held > limit {
		return epoch{}, errors.New("daily authorizations exceed limit")
	}
	delta := uint64(0)
	if refill && u.RX+u.TX+l.chunk > e.Granted {
		delta = min(u.RX+u.TX+l.chunk-e.Granted, limit-held)
	}
	if delta != 0 {
		e.Granted += delta
	}
	if e.RX != u.RX || e.TX != u.TX || delta != 0 {
		e.RX, e.TX = u.RX, u.TX
		_, err = tx.ExecContext(ctx, "UPDATE epochs SET granted=?,rx=?,tx=?,updated_at=? WHERE id=?",
			e.Granted, e.RX, e.TX, time.Now().UTC().Format(time.RFC3339Nano), e.ID)
		if err != nil {
			return epoch{}, err
		}
	}
	return e, tx.Commit()
}

// The old bucket may still receive packets until user_bucket is switched.
func (l *ledger) closeSnapshot(ctx context.Context, id, user uint64, g grant, u usage) error {
	if u.RX > g.Bytes || u.TX > g.Bytes-u.RX {
		return errors.New("usage exceeds kernel authorization")
	}
	result, err := l.db.ExecContext(ctx, `UPDATE epochs SET status='closed',granted=?,rx=?,tx=?,updated_at=?
WHERE id=? AND user_id=? AND granted>=? AND rx<=? AND tx<=?`,
		g.Bytes, u.RX, u.TX, time.Now().UTC().Format(time.RFC3339Nano), id, user, g.Bytes, u.RX, u.TX)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return errors.New("old bucket does not match durable ledger")
	}
	return nil
}

func (l *ledger) historicalEpochs(ctx context.Context, user uint64, day string, current uint64) ([]uint64, error) {
	rows, err := l.db.QueryContext(ctx, "SELECT id FROM epochs WHERE user_id=? AND day<=? AND id!=?", user, day, current)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uint64
	for rows.Next() {
		var id uint64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (l *ledger) report(id, day string) (usageResponse, error) {
	r := usageResponse{UserID: id, Day: day, Limit: l.limit, Allocatable: l.limit, Stale: true}
	d, err := time.ParseInLocation(time.DateOnly, day, shanghai)
	if err != nil {
		return r, err
	}
	r.ResetAt = d.AddDate(0, 0, 1).Format(time.RFC3339)
	var user uint64
	err = l.db.QueryRow("SELECT id FROM users WHERE uuid=?", id).Scan(&user)
	if errors.Is(err, sql.ErrNoRows) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	err = l.db.QueryRow("SELECT limit_bytes FROM user_days WHERE user_id=? AND day=?", user, day).Scan(&r.Limit)
	if errors.Is(err, sql.ErrNoRows) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	err = l.db.QueryRow(`SELECT COALESCE(SUM(rx+tx),0),COALESCE(SUM(granted),0),
COALESCE(SUM(CASE WHEN status='closed' THEN granted-rx-tx ELSE 0 END),0),COALESCE(MAX(updated_at),'')
FROM epochs WHERE user_id=? AND day=?`, user, day).Scan(&r.Confirmed, &r.Held, &r.Uncertain, &r.UpdatedAt)
	if err != nil {
		return r, err
	}
	if r.Held > r.Limit {
		return r, errors.New("ledger exceeds daily limit")
	}
	r.Observed, r.Allocatable = r.Confirmed, r.Limit-r.Held
	// Do not start a new HTTP(S) task with less than 128 bytes left for its
	// handshake/request. Kernel accounting still uses the exact remainder.
	r.Exhausted = r.Allocatable == 0 && r.Limit-r.Confirmed-r.Uncertain < 128
	return r, nil
}
