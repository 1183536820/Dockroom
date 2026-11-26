package fs

import (
	"crypto/rand"
	"errors"
	"golang.org/x/sys/windows"
	"io"
	"os"
	"unsafe"
)

func DiskSource(dir, name string) (*os.File, error) {
	if !Part(name) {
		return nil, ErrUnsafe
	}
	p, e := CatalogDirectory(dir)
	if e != nil {
		return nil, e
	}
	defer p.Close()
	f, e := openNT(p, name, false, windows.FILE_GENERIC_READ, windows.FILE_SHARE_READ, windows.FILE_OPEN, false)
	if e != nil {
		return nil, e
	}
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() {
		f.Close()
		return nil, ErrUnsafe
	}
	return f, nil
}
func DiskReceive(dir, name string, r io.Reader, size int64) error {
	if !Part(name) || size < 0 {
		return ErrUnsafe
	}
	p, e := CatalogDirectory(dir)
	if e != nil {
		return e
	}
	defer p.Close()
	f, e := openNT(p, ".dockroom-"+rand.Text()+".part", false, windows.FILE_GENERIC_WRITE|windows.FILE_READ_ATTRIBUTES|windows.DELETE, 0, windows.FILE_CREATE, false)
	if e != nil {
		return e
	}
	published := false
	defer func() {
		if !published {
			flag := byte(1)
			windows.SetFileInformationByHandle(windows.Handle(f.Fd()), windows.FileDispositionInfo, &flag, uint32(unsafe.Sizeof(flag)))
		}
		f.Close()
	}()
	n, e := io.CopyN(f, r, size)
	if e != nil || n != size {
		return io.ErrUnexpectedEOF
	}
	var one [1]byte
	if n, e := r.Read(one[:]); n != 0 || !errors.Is(e, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = Publish(f, p, name); Collision(e) && e != nil {
		return os.ErrExist
	} else if e != nil {
		return e
	}
	published = true
	return nil
}
func DiskNewDirectory(dir, name string) error {
	if !Part(name) {
		return ErrUnsafe
	}
	p, e := CatalogDirectory(dir)
	if e != nil {
		return e
	}
	defer p.Close()
	child, e := openNT(p, name, true, windows.FILE_GENERIC_READ, share, windows.FILE_CREATE, false)
	if Collision(e) {
		return os.ErrExist
	}
	if e != nil {
		return e
	}
	return child.Close()
}
