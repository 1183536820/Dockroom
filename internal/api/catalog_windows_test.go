package api

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestCatalogListsUnregisteredDirectory(t *testing.T) {
	dir := t.TempDir()
	if e := os.Mkdir(filepath.Join(dir, "未共享目录"), 0700); e != nil {
		t.Fatal(e)
	}
	list := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/catalog?path="+url.QueryEscape(path), nil)
		w := httptest.NewRecorder()
		DirectoryCatalog(w, r)
		return w
	}
	w := list("")
	var drives struct{ Drives []string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &drives) != nil || len(drives.Drives) == 0 {
		t.Fatal("missing drive list")
	}
	w = list(dir)
	var rows struct {
		Entries []struct {
			Name string
			Dir  bool
		}
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &rows) != nil || len(rows.Entries) != 1 || rows.Entries[0].Name != "未共享目录" || !rows.Entries[0].Dir {
		t.Fatalf("listing failed: %s", w.Body.String())
	}
	for _, p := range []string{dir + `\..`, `\\host\share`, `\\?\C:\`} {
		if list(p).Code == 200 {
			t.Fatal("unexpected path accepted")
		}
	}
}
