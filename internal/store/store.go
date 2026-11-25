package store

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS users(id TEXT PRIMARY KEY, password TEXT NOT NULL, admin INTEGER NOT NULL CHECK(admin IN (0,1)), disabled INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS sessions(hash TEXT PRIMARY KEY, owner TEXT NOT NULL REFERENCES users(id), csrf TEXT NOT NULL, expires INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS catalog_sessions(hash TEXT PRIMARY KEY REFERENCES sessions(hash) ON DELETE CASCADE);
CREATE TABLE IF NOT EXISTS disk_join_keys(hash TEXT PRIMARY KEY);
CREATE TABLE IF NOT EXISTS disk_devices(node TEXT PRIMARY KEY REFERENCES nodes(id));
CREATE TABLE IF NOT EXISTS nodes(id TEXT PRIMARY KEY, serial TEXT NOT NULL UNIQUE, fingerprint TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('pending','approved','revoked')), os TEXT NOT NULL DEFAULT '', version TEXT NOT NULL DEFAULT '', last_seen INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS roots(node TEXT NOT NULL REFERENCES nodes(id), id TEXT NOT NULL, label TEXT NOT NULL, available INTEGER NOT NULL, binding TEXT NOT NULL, PRIMARY KEY(node,id));
CREATE TABLE IF NOT EXISTS grants(owner TEXT NOT NULL REFERENCES users(id), node TEXT NOT NULL, root TEXT NOT NULL, PRIMARY KEY(owner,node,root), FOREIGN KEY(node,root) REFERENCES roots(node,id));
CREATE TABLE IF NOT EXISTS enrollments(hash TEXT PRIMARY KEY, node TEXT NOT NULL, expires INTEGER NOT NULL, used INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS pairing_key(slot INTEGER PRIMARY KEY CHECK(slot=1), hash TEXT NOT NULL, owner TEXT NOT NULL REFERENCES users(id));
CREATE TABLE IF NOT EXISTS paired_devices(node TEXT PRIMARY KEY REFERENCES nodes(id), owner TEXT NOT NULL REFERENCES users(id), public_key TEXT NOT NULL, cert BLOB NOT NULL, renew_at INTEGER NOT NULL, auto_grant INTEGER NOT NULL DEFAULT 1);
CREATE TABLE IF NOT EXISTS paired_root_once(node TEXT NOT NULL, root TEXT NOT NULL, PRIMARY KEY(node,root), FOREIGN KEY(node,root) REFERENCES roots(node,id));
CREATE TABLE IF NOT EXISTS device_operators(node TEXT PRIMARY KEY REFERENCES nodes(id), owner TEXT NOT NULL UNIQUE REFERENCES users(id));
CREATE TABLE IF NOT EXISTS audit(id INTEGER PRIMARY KEY, utc INTEGER NOT NULL, actor TEXT NOT NULL, action TEXT NOT NULL, ref TEXT NOT NULL, result TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS copy_plans(id TEXT PRIMARY KEY, owner TEXT NOT NULL REFERENCES users(id), expires INTEGER NOT NULL, payload BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS copy_jobs(id TEXT PRIMARY KEY, owner TEXT NOT NULL REFERENCES users(id), plan TEXT NOT NULL UNIQUE REFERENCES copy_plans(id), idem TEXT NOT NULL, created INTEGER NOT NULL, due INTEGER NOT NULL, state TEXT NOT NULL, stopped INTEGER NOT NULL DEFAULT 0, payload BLOB NOT NULL, UNIQUE(owner,idem));
CREATE INDEX IF NOT EXISTS copy_schedule ON copy_jobs(state,due,created);
`

func Open(dir string, create bool) (*sql.DB, error) {
	p := filepath.Join(dir, "hub.db")
	if !create {
		if _, e := os.Stat(p); e != nil {
			return nil, e
		}
	}
	db, e := sql.Open("sqlite", p)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000", "PRAGMA synchronous=FULL", schema} {
		if _, e = db.Exec(q); e != nil {
			db.Close()
			return nil, e
		}
	}
	var mode string
	if e = db.QueryRow("PRAGMA journal_mode").Scan(&mode); e != nil || mode != "wal" {
		db.Close()
		return nil, errors.New("SQLite WAL unavailable")
	}
	// Pre-checkpoint databases have no binding and cannot safely inherit grants.
	var n int
	if e = db.QueryRow(`SELECT count(*) FROM pragma_table_info('roots') WHERE name='binding'`).Scan(&n); e != nil || n != 1 {
		db.Close()
		return nil, errors.New("unbound pre-checkpoint database; preserve it and initialize fresh state")
	}
	for _, c := range [][2]string{{"roots", "copy_write"}, {"grants", "copy_write"}} {
		if e = db.QueryRow(`SELECT count(*) FROM pragma_table_info(?) WHERE name=?`, c[0], c[1]).Scan(&n); e != nil {
			db.Close()
			return nil, e
		}
		if n == 0 {
			if _, e = db.Exec(`ALTER TABLE ` + c[0] + ` ADD COLUMN copy_write INTEGER NOT NULL DEFAULT 0`); e != nil {
				db.Close()
				return nil, e
			}
		}
	}
	// Existing DG2 primary roots already consumed their implicit grant. Never restore
	// one that a host administrator previously revoked.
	if _, e = db.Exec(`INSERT OR IGNORE INTO paired_root_once(node,root) SELECT p.node,r.id FROM paired_devices p JOIN roots r ON r.node=p.node WHERE r.id='files' AND p.auto_grant=0`); e != nil {
		db.Close()
		return nil, e
	}
	if e = os.Chmod(p, 0600); e != nil {
		db.Close()
		return nil, e
	}
	return db, nil
}
