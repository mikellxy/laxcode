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
PRAGMA user_version=1;
