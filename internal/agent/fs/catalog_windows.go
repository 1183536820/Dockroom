package fs

import (
	"golang.org/x/sys/windows"
	"os"
)

// A separate, directory-only interface. Drive roots never become transfer roots.
func CatalogDrives() ([]string, error) {
	mask, e := windows.GetLogicalDrives()
	if e != nil {
		return nil, e
	}
	out := []string{}
	for i := 0; i < 26; i++ {
		if mask&(1<<i) == 0 {
			continue
		}
		d := string(rune('A'+i)) + `:\`
		p, _ := windows.UTF16PtrFromString(d)
		if windows.GetDriveType(p) == windows.DRIVE_FIXED {
			out = append(out, d)
		}
	}
	return out, nil
}
func CatalogDirectory(path string) (*os.File, error) {
	if len(path) < 3 || path[0] < 'A' || path[0] > 'Z' || path[1:3] != `:\` {
		return nil, ErrUnsafe
	}
	f, e := start(path[:3])
	if e != nil {
		return nil, e
	}
	if len(path) == 3 {
		return f, nil
	}
	parts, err := catalogParts(path[3:])
	if err != nil {
		f.Close()
		return nil, err
	}
	for _, p := range parts {
		child, e := open(f, p, true, false, false)
		f.Close()
		if e != nil {
			return nil, e
		}
		f = child
	}
	return f, nil
}
