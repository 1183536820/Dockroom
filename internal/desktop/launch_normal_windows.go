//go:build !dockroom_fixture

package desktop

import "os"

func desktopEnvironment() (string, func(string) error, error) {
	p, e := os.UserCacheDir()
	return p, openBrowser, e
}
