package fs

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func fixture(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(os.Getenv("DOCKROOM_TEST_BASE"), "dockroom-fs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	return dir
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func pin(t *testing.T, p string) Root { t.Helper(); id, e := Pin(p); must(t, e); return Root{p, id} }

func TestBoundary(t *testing.T) {
	dir := fixture(t)
	inside := filepath.Join(dir, "root")
	outside := filepath.Join(dir, "outside")
	for _, d := range []string{inside, outside, filepath.Join(inside, Private), filepath.Join(inside, "空目录")} {
		must(t, os.Mkdir(d, 0700))
	}
	must(t, os.WriteFile(filepath.Join(outside, "sentinel"), []byte("outside"), 0600))
	link := filepath.Join(inside, "jump")
	if runtime.GOOS == "windows" {
		out, e := exec.Command("cmd.exe", "/c", "mklink", "/J", link, outside).CombinedOutput()
		if e != nil {
			t.Fatalf("create fixture junction: %v: %s", e, out)
		}
	} else {
		must(t, os.Symlink(outside, link))
	}
	r := pin(t, inside)
	for _, part := range []string{"jump", Private, "..", "../outside", "/", `C:foo`, "\xff"} {
		if f, e := r.OpenDir([]string{part}); e == nil {
			f.Close()
			t.Errorf("opened forbidden component %q", part)
		}
	}
	f, e := r.OpenDir([]string{"空目录"})
	must(t, e)
	f.Close()
	if _, e = r.PartsFromPath(outside); e == nil {
		t.Error("outside absolute path translated")
	}
	if Validate([]Root{r, pin(t, filepath.Join(inside, "空目录"))}) == nil {
		t.Fatal("accepted overlap")
	}
	must(t, os.Rename(inside, filepath.Join(dir, "original")))
	must(t, os.Mkdir(inside, 0700))
	if f, e = r.OpenDir(nil); e == nil {
		f.Close()
		t.Fatal("accepted replacement root")
	}
}

func TestNoClobberAndRestore(t *testing.T) {
	dir := fixture(t)
	root := pin(t, dir)
	pub := filepath.Join(dir, "visible")
	priv := filepath.Join(dir, "fixture-private")
	must(t, os.Mkdir(pub, 0700))
	must(t, os.Mkdir(priv, 0700))
	a, e := root.OpenDir([]string{"fixture-private"})
	must(t, e)
	defer a.Close()
	b, e := root.OpenDir([]string{"visible"})
	must(t, e)
	defer b.Close()
	write := func(d, n, s string) { t.Helper(); must(t, os.WriteFile(filepath.Join(d, n), []byte(s), 0600)) }
	read := func(d, n, s string) {
		t.Helper()
		data, e := os.ReadFile(filepath.Join(d, n))
		must(t, e)
		if string(data) != s {
			t.Fatal("unexpected contents")
		}
	}
	write(priv, "stage", "new")
	write(pub, "occupied", "old")
	if moveExclusive(a, "stage", b, "occupied") == nil {
		t.Fatal("overwrote occupied destination")
	}
	read(pub, "occupied", "old")
	read(priv, "stage", "new")
	must(t, moveExclusive(a, "stage", b, "published"))
	read(pub, "published", "new")
	must(t, os.Mkdir(filepath.Join(pub, "待回收"), 0700))
	write(filepath.Join(pub, "待回收"), "leaf", "keep")
	id := pin(t, filepath.Join(pub, "待回收")).Identity
	must(t, moveExclusive(b, "待回收", a, "trash-id"))
	must(t, os.Mkdir(filepath.Join(pub, "待回收"), 0700))
	if moveExclusive(a, "trash-id", b, "待回收") == nil {
		t.Fatal("overwrote restore conflict")
	}
	read(filepath.Join(priv, "trash-id"), "leaf", "keep")
	must(t, os.Remove(filepath.Join(pub, "待回收")))
	must(t, moveExclusive(a, "trash-id", b, "待回收"))
	if id != pin(t, filepath.Join(pub, "待回收")).Identity {
		t.Fatal("restore changed object identity")
	}
	read(filepath.Join(pub, "待回收"), "leaf", "keep")
	for i := 0; i < 8; i++ {
		write(priv, fmt.Sprint(i), fmt.Sprint(i))
	}
	done := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func(i int) { done <- moveExclusive(a, fmt.Sprint(i), b, "race") }(i)
	}
	wins := 0
	for range 8 {
		if <-done == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("%d publishers won", wins)
	}
}
