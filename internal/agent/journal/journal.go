// Package journal is the M2 destination persistence primitive. Callers must
// supply a staging handle from the authorized same-volume private area; this
// package neither resolves user paths nor publishes or deletes files.
package journal

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"dockroom/internal/agent/fs"
	_ "modernc.org/sqlite"
)

var ErrMismatch = errors.New("digest, identity, length, or offset mismatch")
var ErrState = errors.New("journal state does not allow this operation")

const blockMax = 8 << 20

type Journal struct {
	db *sql.DB
	mu sync.Mutex
}
type Item struct {
	ID, Identity string
	Size         int64
}
type Intent struct{ ItemID, ParentIdentity, Leaf, Identity, Digest string }
type Evidence struct {
	Status           Observation
	Identity, Digest string
}

// Unknown includes lookup failure, unsafe parent, or any ambiguous state.
type Observation uint8

const (
	Unknown Observation = iota
	Absent
	Present
)

type Decision string

const (
	Committed   Decision = "committed"
	RetryCommit Decision = "retry_commit"
	NeedsReview Decision = "needs_review"
)

func Open(stateDir string) (*Journal, error) {
	db, e := sql.Open("sqlite", filepath.Join(stateDir, "transfer-journal.db"))
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	var mode string
	if e = db.QueryRow("PRAGMA journal_mode=WAL").Scan(&mode); e != nil || mode != "wal" {
		db.Close()
		return nil, errors.New("journal WAL unavailable")
	}
	for _, q := range []string{"PRAGMA synchronous=FULL", "PRAGMA foreign_keys=ON", `
CREATE TABLE IF NOT EXISTS stages(id TEXT PRIMARY KEY, identity TEXT NOT NULL, size INTEGER NOT NULL, prefix INTEGER NOT NULL DEFAULT 0, verified TEXT NOT NULL DEFAULT '', state TEXT NOT NULL DEFAULT 'staging');
CREATE TABLE IF NOT EXISTS blocks(item TEXT NOT NULL REFERENCES stages(id), off INTEGER NOT NULL, length INTEGER NOT NULL, sha TEXT NOT NULL, PRIMARY KEY(item,off));
CREATE TABLE IF NOT EXISTS intents(item TEXT PRIMARY KEY REFERENCES stages(id), parent TEXT NOT NULL, leaf TEXT NOT NULL, identity TEXT NOT NULL, sha TEXT NOT NULL);
`} {
		if _, e = db.Exec(q); e != nil {
			db.Close()
			return nil, e
		}
	}
	if e = os.Chmod(filepath.Join(stateDir, "transfer-journal.db"), 0600); e != nil {
		db.Close()
		return nil, e
	}
	return &Journal{db: db}, nil
}
func (j *Journal) Close() error { return j.db.Close() }
func (j *Journal) Begin(x Item) error {
	if x.ID == "" || x.Identity == "" || x.Size < 0 {
		return ErrMismatch
	}
	_, e := j.db.Exec(`INSERT INTO stages(id,identity,size) VALUES(?,?,?)`, x.ID, x.Identity, x.Size)
	return e
}

func (j *Journal) Find(id string) (Item, string, error) {
	x := Item{ID: id}
	var state string
	e := j.db.QueryRow(`SELECT identity,size,state FROM stages WHERE id=?`, id).Scan(&x.Identity, &x.Size, &state)
	return x, state, e
}

func (j *Journal) Intent(id string) (Intent, error) {
	x := Intent{ItemID: id}
	e := j.db.QueryRow(`SELECT parent,leaf,identity,sha FROM intents WHERE item=?`, id).Scan(&x.ParentIdentity, &x.Leaf, &x.Identity, &x.Digest)
	return x, e
}
func stage(q interface{ QueryRow(string, ...any) *sql.Row }, x Item) (int64, string, error) {
	var identity, state string
	var prefix, size int64
	e := q.QueryRow(`SELECT identity,size,prefix,state FROM stages WHERE id=?`, x.ID).Scan(&identity, &size, &prefix, &state)
	if e != nil {
		return 0, "", e
	}
	if identity != x.Identity || size != x.Size || prefix < 0 || prefix > size {
		return 0, "", ErrMismatch
	}
	return prefix, state, nil
}
func hexSHA(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && s == strings.ToLower(s)
}

func checkHandle(f *os.File, x Item) error {
	id, e := fs.ObjectIdentity(f)
	if e != nil {
		return e
	}
	if id != x.Identity {
		return ErrMismatch
	}
	return nil
}
func sum(src io.Reader, n int64) (string, error) {
	h := sha256.New()
	actual, e := io.CopyBuffer(h, io.LimitReader(src, n), make([]byte, 32<<10))
	if e != nil {
		return "", e
	}
	if actual != n {
		return "", io.ErrUnexpectedEOF
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Recover verifies the entire persisted prefix, and discards only the tail of
// the caller-owned staging handle. No truncation is allowed after intent.
func (j *Journal) Recover(x Item, f *os.File) (int64, error) {
	if e := checkHandle(f, x); e != nil {
		return 0, e
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	p, state, e := stage(j.db, x)
	if e != nil {
		return 0, e
	}
	if state != "staging" && state != "verified" {
		return 0, ErrState
	}
	i, e := f.Stat()
	if e != nil {
		return 0, e
	}
	if !i.Mode().IsRegular() || i.Size() < p {
		return 0, ErrMismatch
	}
	rows, e := j.db.Query(`SELECT off,length,sha FROM blocks WHERE item=? ORDER BY off`, x.ID)
	if e != nil {
		return 0, e
	}
	defer rows.Close()
	var offset int64
	for rows.Next() {
		var off, n int64
		var expected string
		if e = rows.Scan(&off, &n, &expected); e != nil {
			return 0, e
		}
		if off != offset || n < 1 || n > blockMax || n > p-off {
			return 0, ErrMismatch
		}
		h, e := sum(io.NewSectionReader(f, off, n), n)
		if e != nil {
			return 0, e
		}
		if h != expected {
			return 0, ErrMismatch
		}
		offset += n
	}
	if e = rows.Err(); e != nil {
		return 0, e
	}
	if offset != p {
		return 0, ErrMismatch
	}
	if i.Size() > p {
		if e = f.Truncate(p); e != nil {
			return 0, e
		}
		if e = f.Sync(); e != nil {
			return 0, e
		}
	}
	return p, nil
}

// Append acknowledges only after independently hashing the staged block,
// syncing the file, and committing the SQLite waterline. The reader belongs
// to a length-checked relay request for just this block.
func (j *Journal) Append(x Item, f *os.File, off, n int64, expected string, src io.Reader) (int64, error) {
	if off < 0 || n < 1 || n > blockMax || off > x.Size || n > x.Size-off || !hexSHA(expected) || src == nil {
		return 0, ErrMismatch
	}
	if e := checkHandle(f, x); e != nil {
		return 0, e
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	p, state, e := stage(j.db, x)
	if e != nil {
		return 0, e
	}
	if state != "staging" {
		return p, ErrState
	}
	if off < p {
		var length int64
		var hash string
		if e = j.db.QueryRow(`SELECT length,sha FROM blocks WHERE item=? AND off=?`, x.ID, off).Scan(&length, &hash); e != nil || n != length || expected != hash {
			return p, ErrMismatch
		}
		verified, e := sum(io.NewSectionReader(f, off, n), n)
		if e != nil {
			return p, e
		}
		if verified != expected {
			return p, ErrMismatch
		}
		return p, nil
	}
	if p != off {
		return p, ErrMismatch
	}
	i, e := f.Stat()
	if e != nil {
		return p, e
	}
	if !i.Mode().IsRegular() || i.Size() != p {
		return p, ErrMismatch
	}
	h := sha256.New()
	writer := io.NewOffsetWriter(f, off)
	count, e := io.CopyBuffer(io.MultiWriter(writer, h), io.LimitReader(src, n), make([]byte, 32<<10))
	if e != nil {
		return p, e
	}
	if count != n || hex.EncodeToString(h.Sum(nil)) != expected {
		return p, ErrMismatch
	}
	if e = f.Sync(); e != nil {
		return p, e
	}
	tx, e := j.db.Begin()
	if e != nil {
		return p, e
	}
	defer tx.Rollback()
	if _, e = tx.Exec(`INSERT INTO blocks(item,off,length,sha) VALUES(?,?,?,?)`, x.ID, off, n, expected); e != nil {
		return p, e
	}
	if _, e = tx.Exec(`UPDATE stages SET prefix=? WHERE id=?`, off+n, x.ID); e != nil {
		return p, e
	}
	if e = tx.Commit(); e != nil {
		return p, e
	}
	return off + n, nil
}

// Verify rereads the complete staging object, including the zero-byte case.
func (j *Journal) Verify(x Item, f *os.File, expected string) error {
	if !hexSHA(expected) {
		return ErrMismatch
	}
	if e := checkHandle(f, x); e != nil {
		return e
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	p, state, e := stage(j.db, x)
	if e != nil {
		return e
	}
	if state != "staging" || p != x.Size {
		return ErrState
	}
	i, e := f.Stat()
	if e != nil {
		return e
	}
	if !i.Mode().IsRegular() || i.Size() != p {
		return ErrMismatch
	}
	actual, e := sum(io.NewSectionReader(f, 0, p), p)
	if e != nil {
		return e
	}
	if actual != expected {
		return ErrMismatch
	}
	if e = f.Sync(); e != nil {
		return e
	}
	_, e = j.db.Exec(`UPDATE stages SET verified=?,state='verified' WHERE id=?`, actual, x.ID)
	return e
}
func (j *Journal) RecordIntent(v Intent) error {
	if !fs.Part(v.Leaf) || v.ParentIdentity == "" || !hexSHA(v.Digest) {
		return ErrMismatch
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	tx, e := j.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var id, digest, state string
	if e = tx.QueryRow(`SELECT identity,verified,state FROM stages WHERE id=?`, v.ItemID).Scan(&id, &digest, &state); e != nil {
		return e
	}
	if state != "verified" {
		return ErrState
	}
	if id != v.Identity || digest != v.Digest {
		return ErrMismatch
	}
	if _, e = tx.Exec(`INSERT INTO intents VALUES(?,?,?,?,?)`, v.ItemID, v.ParentIdentity, v.Leaf, v.Identity, v.Digest); e != nil {
		return e
	}
	if _, e = tx.Exec(`UPDATE stages SET state='intent' WHERE id=?`, v.ItemID); e != nil {
		return e
	}
	return tx.Commit()
}

func (j *Journal) Pending() ([]Intent, error) {
	rows, e := j.db.Query(`SELECT intents.item,parent,leaf,intents.identity,sha FROM intents JOIN stages ON stages.id=intents.item WHERE stages.state='intent' ORDER BY intents.item`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var pending []Intent
	for rows.Next() {
		var v Intent
		if e = rows.Scan(&v.ItemID, &v.ParentIdentity, &v.Leaf, &v.Identity, &v.Digest); e != nil {
			return nil, e
		}
		pending = append(pending, v)
	}
	return pending, rows.Err()
}

// Reconcile may persist a receipt only when the exact persisted intent is
// observed. Probe inputs must come from freshly validated, pinned handles;
// the caller is responsible for the platform's publish durability barrier.
func (j *Journal) Reconcile(v Intent, stage, destination Evidence) (Decision, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	var got Intent
	got.ItemID = v.ItemID
	var state string
	e := j.db.QueryRow(`SELECT parent,leaf,intents.identity,sha,state FROM intents JOIN stages ON stages.id=intents.item WHERE intents.item=?`, v.ItemID).Scan(&got.ParentIdentity, &got.Leaf, &got.Identity, &got.Digest, &state)
	if e != nil {
		return NeedsReview, e
	}
	if got != v {
		return NeedsReview, ErrMismatch
	}
	if state == "committed" {
		return Committed, nil
	}
	if state == "needs_review" {
		return NeedsReview, nil
	}
	if state != "intent" {
		return NeedsReview, ErrState
	}
	match := func(ev Evidence) bool {
		return ev.Status == Present && ev.Identity == v.Identity && ev.Digest == v.Digest
	}
	if stage.Status == Absent && match(destination) {
		if _, e = j.db.Exec(`UPDATE stages SET state='committed' WHERE id=?`, v.ItemID); e != nil {
			return NeedsReview, e
		}
		return Committed, nil
	}
	if match(stage) && destination.Status == Absent {
		return RetryCommit, nil
	}
	_, e = j.db.Exec(`UPDATE stages SET state='needs_review' WHERE id=?`, v.ItemID)
	return NeedsReview, e
}
