package fs

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func platformPart(string) bool { return true }

func start(base string) (*os.File, error) {
	fd, err := unix.Open(base, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), base), nil
}

func open(parent *os.File, name string, dir, _ bool, _ bool) (*os.File, error) {
	flags := uint64(unix.O_PATH | unix.O_CLOEXEC)
	if dir {
		flags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC
	}
	resolve := uint64(unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV)
	fd, err := unix.Openat2(int(parent.Fd()), name, &unix.OpenHow{Flags: flags, Resolve: resolve})
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	info, err := f.Stat()
	if err != nil || !(info.IsDir() || info.Mode().IsRegular()) {
		f.Close()
		return nil, ErrUnsafe
	}
	return f, nil
}

func identify(f *os.File) (identity, string, error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return identity{}, "", err
	}
	return identity{fmt.Sprintf("%x", st.Dev), fmt.Sprintf("%x", st.Ino)}, fmt.Sprintf("%d.%d", st.Ctim.Sec, st.Ctim.Nsec), nil
}
