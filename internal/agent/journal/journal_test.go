package journal

import (
	"crypto/sha256"
	"dockroom/internal/agent/fs"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func digest(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }
func needed(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func prepared(t *testing.T, dir, id string, n int64) (*os.File, Item) {
	t.Helper()
	f, e := os.OpenFile(filepath.Join(dir, id), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	needed(t, e)
	t.Cleanup(func() { f.Close() })
	ident, e := fs.ObjectIdentity(f)
	needed(t, e)
	return f, Item{id, ident, n}
}
func TestConfirmedPrefixAndLostAcknowledgement(t *testing.T) {
	dir := t.TempDir()
	j, e := Open(dir)
	needed(t, e)
	t.Cleanup(func() { j.Close() })
	f, x := prepared(t, dir, "stage", 6)
	needed(t, j.Begin(x))
	p, e := j.Append(x, f, 0, 3, digest("abc"), strings.NewReader("abc"))
	needed(t, e)
	if p != 3 {
		t.Fatal(p)
	}
	p, e = j.Append(x, f, 0, 3, digest("abc"), strings.NewReader(""))
	needed(t, e)
	if p != 3 {
		t.Fatal(p)
	}
	if _, e = j.Append(x, f, 3, 3, digest("def"), strings.NewReader("BAD")); !errors.Is(e, ErrMismatch) {
		t.Fatalf("accepted wrong block: %v", e)
	}
	needed(t, f.Sync())
	needed(t, j.Close())
	j, e = Open(dir)
	needed(t, e)
	p, e = j.Recover(x, f)
	needed(t, e)
	if p != 3 {
		t.Fatal(p)
	}
	info, e := f.Stat()
	needed(t, e)
	if info.Size() != 3 {
		t.Fatalf("unconfirmed tail preserved: %d", info.Size())
	}
	wrong, _ := prepared(t, dir, "imposter-stage", 6)
	if _, e = j.Recover(x, wrong); !errors.Is(e, ErrMismatch) {
		t.Fatalf("accepted a different object: %v", e)
	}
	p, e = j.Append(x, f, 3, 3, digest("def"), strings.NewReader("def"))
	needed(t, e)
	if p != 6 {
		t.Fatal(p)
	}
	if e = j.Verify(x, f, digest("wrong")); !errors.Is(e, ErrMismatch) {
		t.Fatalf("wrong final digest: %v", e)
	}
	needed(t, j.Verify(x, f, digest("abcdef")))
	v := Intent{x.ID, "parent-object", "文件.txt", x.Identity, digest("abcdef")}
	needed(t, j.RecordIntent(v))
	if _, e = j.Recover(x, f); !errors.Is(e, ErrState) {
		t.Fatalf("truncation allowed after intent: %v", e)
	}
	needed(t, j.Close())
	j, e = Open(dir)
	needed(t, e)
	pending, e := j.Pending()
	needed(t, e)
	if len(pending) != 1 || pending[0] != v {
		t.Fatalf("lost intent: %v", pending)
	}
	retry, e := j.Reconcile(v, Evidence{Present, v.Identity, v.Digest}, Evidence{Status: Absent})
	needed(t, e)
	if retry != RetryCommit {
		t.Fatal(retry)
	}
	actual, e := j.Reconcile(v, Evidence{Status: Absent}, Evidence{Present, v.Identity, v.Digest})
	needed(t, e)
	if actual != Committed {
		t.Fatal(actual)
	}
	needed(t, j.Close())
	j, e = Open(dir)
	needed(t, e)
	actual, e = j.Reconcile(v, Evidence{}, Evidence{})
	needed(t, e)
	if actual != Committed {
		t.Fatal("persisted receipt not returned")
	}
}

func TestZeroLengthAndAmbiguousReceipt(t *testing.T) {
	dir := t.TempDir()
	j, e := Open(dir)
	needed(t, e)
	defer j.Close()
	for _, name := range []string{"same-hash-different-object", "both-present", "unknown-parent"} {
		t.Run(name, func(t *testing.T) {
			f, x := prepared(t, dir, name, 0)
			needed(t, j.Begin(x))
			full := digest("")
			needed(t, j.Verify(x, f, full))
			v := Intent{x.ID, "parent", "target", x.Identity, full}
			needed(t, j.RecordIntent(v))
			stage, final := Evidence{Status: Absent}, Evidence{Present, x.Identity, full}
			switch name {
			case "same-hash-different-object":
				final.Identity = "somebody-elses-file"
			case "both-present":
				stage = final
			case "unknown-parent":
				stage.Status = Unknown
			}
			decision, e := j.Reconcile(v, stage, final)
			needed(t, e)
			if decision != NeedsReview {
				t.Fatal(decision)
			}
			decision, e = j.Reconcile(v, Evidence{Status: Absent}, Evidence{Present, x.Identity, full})
			needed(t, e)
			if decision != NeedsReview {
				t.Fatal("ambiguous intent replayed automatically")
			}
		})
	}
}
