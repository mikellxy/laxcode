package main

import (
	"database/sql"
	"errors"
	"fmt"
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
	ID, User                           uint64
	UUID, Day, MapSet, Status          string
	Granted, RX, TX, Version, Deadline uint64
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
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return nil, err
	}
	if version != 0 && version != 1 {
		return nil, fmt.Errorf("unsupported ledger schema %d", version)
	}
	_, err = db.Exec(`
CREATE TABLE IF NOT EXISTS metadata (
 id INTEGER PRIMARY KEY CHECK(id=1), db_id TEXT NOT NULL, map_set TEXT NOT NULL DEFAULT '',
 boot_id TEXT NOT NULL DEFAULT '', last_day TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS users (id INTEGER PRIMARY KEY AUTOINCREMENT, uuid TEXT NOT NULL UNIQUE);
CREATE TABLE IF NOT EXISTS user_days (
 user_id INTEGER NOT NULL REFERENCES users(id), day TEXT NOT NULL,
 limit_bytes INTEGER NOT NULL CHECK(limit_bytes>0), PRIMARY KEY(user_id,day)
);
CREATE TABLE IF NOT EXISTS epochs (
 id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, day TEXT NOT NULL,
 map_set TEXT NOT NULL, status TEXT NOT NULL CHECK(status IN ('active','closed','unknown')),
 granted INTEGER NOT NULL DEFAULT 0 CHECK(granted>=0),
 rx INTEGER NOT NULL DEFAULT 0 CHECK(rx>=0), tx INTEGER NOT NULL DEFAULT 0 CHECK(tx>=0),
 version INTEGER NOT NULL DEFAULT 0 CHECK(version>=0), deadline INTEGER NOT NULL,
 updated_at TEXT NOT NULL,
 FOREIGN KEY(user_id,day) REFERENCES user_days(user_id,day),
 CHECK(rx+tx<=granted)
);
CREATE UNIQUE INDEX IF NOT EXISTS active_epoch ON epochs(user_id,day) WHERE status='active';
PRAGMA user_version=1;`)
	if err != nil {
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

// Called only after kernel identity/ABI verification. Missing kernel state never
// returns an old epoch's unobserved authorization to the daily balance.
func (l *ledger) adopt(mapSet, boot string, fresh bool) error {
	tx, err := l.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var oldMap, oldBoot string
	if err = tx.QueryRow("SELECT map_set,boot_id FROM metadata WHERE id=1").Scan(&oldMap, &oldBoot); err != nil {
		return err
	}
	if oldMap == mapSet && oldBoot == boot {
		return tx.Commit()
	}
	if !fresh {
		return errors.New("pinned map-set and ledger identity disagree")
	}
	if _, err = tx.Exec("UPDATE epochs SET status='unknown' WHERE status='active'"); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE metadata SET map_set=?,boot_id=? WHERE id=1", mapSet, boot); err != nil {
		return err
	}
	return tx.Commit()
}

const epochColumns = "e.id,e.user_id,u.uuid,e.day,e.map_set,e.status,e.granted,e.rx,e.tx,e.version,e.deadline"

func scanEpoch(row interface{ Scan(...any) error }) (e epoch, err error) {
	err = row.Scan(&e.ID, &e.User, &e.UUID, &e.Day, &e.MapSet, &e.Status, &e.Granted, &e.RX, &e.TX, &e.Version, &e.Deadline)
	return
}
func (l *ledger) active() ([]epoch, error) {
	rows, err := l.db.Query("SELECT " + epochColumns + " FROM epochs e JOIN users u ON u.id=e.user_id WHERE e.status='active'")
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

func (l *ledger) ensure(id, day, mapSet string, deadline uint64) (epoch, error) {
	tx, err := l.db.Begin()
	if err != nil {
		return epoch{}, err
	}
	defer tx.Rollback()
	var last, current string
	if err = tx.QueryRow("SELECT last_day,map_set FROM metadata WHERE id=1").Scan(&last, &current); err != nil {
		return epoch{}, err
	}
	if current != mapSet || day < last {
		return epoch{}, errors.New("stale map-set or backwards accounting date")
	}
	if _, err = tx.Exec("UPDATE metadata SET last_day=? WHERE id=1", day); err != nil {
		return epoch{}, err
	}
	if _, err = tx.Exec("INSERT OR IGNORE INTO users(uuid) VALUES(?)", id); err != nil {
		return epoch{}, err
	}
	var user uint64
	if err = tx.QueryRow("SELECT id FROM users WHERE uuid=?", id).Scan(&user); err != nil {
		return epoch{}, err
	}
	if _, err = tx.Exec("INSERT OR IGNORE INTO user_days(user_id,day,limit_bytes) VALUES(?,?,?)", user, day, l.limit); err != nil {
		return epoch{}, err
	}
	var closed int
	if err = tx.QueryRow("SELECT count(*) FROM epochs WHERE user_id=? AND day=? AND status='closed'", user, day).Scan(&closed); err != nil {
		return epoch{}, err
	}
	if closed != 0 {
		return epoch{}, errors.New("accounting date is already closed")
	}
	_, err = tx.Exec(`INSERT INTO epochs(user_id,day,map_set,status,deadline,updated_at)
SELECT ?,?,?,'active',?,? WHERE NOT EXISTS(SELECT 1 FROM epochs WHERE user_id=? AND day=? AND status='active')`,
		user, day, mapSet, deadline, time.Now().UTC().Format(time.RFC3339Nano), user, day)
	if err != nil {
		return epoch{}, err
	}
	e, err := scanEpoch(tx.QueryRow("SELECT "+epochColumns+" FROM epochs e JOIN users u ON u.id=e.user_id WHERE e.user_id=? AND e.day=? AND e.status='active'", user, day))
	if err != nil {
		return epoch{}, err
	}
	if e.MapSet != mapSet {
		return epoch{}, errors.New("epoch belongs to another map-set")
	}
	return e, tx.Commit()
}

// Persist cumulative authorization before exposing it to the kernel. Usage is a
// locked cumulative snapshot, never a delta that can be applied twice.
func (l *ledger) checkpoint(id uint64, mapSet string, u usage, refill bool) (epoch, error) {
	tx, err := l.db.Begin()
	if err != nil {
		return epoch{}, err
	}
	defer tx.Rollback()
	e, err := scanEpoch(tx.QueryRow("SELECT "+epochColumns+" FROM epochs e JOIN users u ON u.id=e.user_id WHERE e.id=?", id))
	if err != nil {
		return epoch{}, err
	}
	if e.MapSet != mapSet || e.Status != "active" || u.RX < e.RX || u.TX < e.TX ||
		u.RX > e.Granted || u.TX > e.Granted-u.RX {
		return epoch{}, errors.New("usage snapshot violates epoch invariants")
	}
	var held, limit uint64
	if err = tx.QueryRow("SELECT COALESCE(SUM(granted),0) FROM epochs WHERE user_id=? AND day=?", e.User, e.Day).Scan(&held); err != nil {
		return epoch{}, err
	}
	if err = tx.QueryRow("SELECT limit_bytes FROM user_days WHERE user_id=? AND day=?", e.User, e.Day).Scan(&limit); err != nil {
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
		e.Version++
	}
	if e.RX != u.RX || e.TX != u.TX || delta != 0 {
		e.RX, e.TX = u.RX, u.TX
		_, err = tx.Exec("UPDATE epochs SET granted=?,rx=?,tx=?,version=?,updated_at=? WHERE id=?",
			e.Granted, e.RX, e.TX, e.Version, time.Now().UTC().Format(time.RFC3339Nano), e.ID)
		if err != nil {
			return epoch{}, err
		}
	}
	return e, tx.Commit()
}

func (l *ledger) closeEpoch(id uint64) error {
	_, err := l.db.Exec("UPDATE epochs SET status='closed' WHERE id=? AND status='active'", id)
	return err
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
COALESCE(SUM(CASE WHEN status='unknown' THEN granted-rx-tx ELSE 0 END),0),COALESCE(MAX(updated_at),'')
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
