package fs

import (
	"golang.org/x/sys/unix"
	"os"
)

func moveExclusive(from *os.File, old string, to *os.File, name string) error {
	if !Part(old) || !Part(name) {
		return ErrUnsafe
	}
	return unix.Renameat2(int(from.Fd()), old, int(to.Fd()), name, unix.RENAME_NOREPLACE)
}
