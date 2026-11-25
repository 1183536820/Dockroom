//go:build dockroom_fixture

package desktop

import (
	"fmt"
	"os"
	"path/filepath"
)

func desktopEnvironment() (string, func(string) error, error) {
	if p := os.Getenv("DOCKROOM_TEST_DESKTOP_BASE"); filepath.IsAbs(p) {
		return p, func(s string) error { fmt.Println(s); return nil }, nil
	}
	p, e := os.UserCacheDir()
	return p, openBrowser, e
}
