package fs

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const CanWrite = true

func policy(f *os.File) (string, error) {
	u, e := windows.GetCurrentProcessToken().GetTokenUser()
	if e != nil {
		return "", e
	}
	trusted := func(s *windows.SID) bool {
		v := s.String()
		return v == u.User.Sid.String() || v == "S-1-5-18" || v == "S-1-5-32-544"
	}
	sd, e := windows.GetSecurityInfo(windows.Handle(f.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if e != nil {
		return "", e
	}
	owner, _, e := sd.Owner()
	if e != nil || !trusted(owner) {
		return "", ErrUnsafe
	}
	acl, _, e := sd.DACL()
	if e != nil || acl == nil || acl.AceCount == 0 {
		return "", ErrUnsafe
	}
	var filePolicy []string
	for i := range uint32(acl.AceCount) {
		var a *windows.ACCESS_ALLOWED_ACE
		if e = windows.GetAce(acl, i, &a); e != nil {
			return "", e
		}
		if a.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || a.Header.AceSize < 16 {
			return "", ErrUnsafe
		}
		sid := (*windows.SID)(unsafe.Pointer(&a.SidStart))
		if !trusted(sid) {
			return "", ErrUnsafe
		}
		const objectInherit, noPropagate = 1, 4
		if a.Header.AceFlags&objectInherit != 0 {
			if a.Header.AceFlags&noPropagate != 0 {
				return "", ErrUnsafe
			}
			filePolicy = append(filePolicy, fmt.Sprintf("%s:%x", sid.String(), a.Mask))
		}
	}
	slices.Sort(filePolicy)
	return strings.Join(filePolicy, "/"), nil
}

func openWriteDir(r Root, parts []string) (*os.File, string, error) {
	if len(parts) > 128 {
		return nil, "", ErrUnsafe
	}
	f, e := r.OpenDir(nil)
	if e != nil {
		return nil, "", e
	}
	for _, p := range parts {
		if !Part(p) {
			f.Close()
			return nil, "", ErrUnsafe
		}
		if _, e = policy(f); e != nil {
			f.Close()
			return nil, "", e
		}
		next, e := open(f, p, true, false, false)
		f.Close()
		if e != nil {
			return nil, "", e
		}
		f = next
	}
	acl, e := policy(f)
	if e != nil || acl == "" {
		f.Close()
		return nil, "", ErrUnsafe
	}
	return f, acl, nil
}

func PrepareWrite(r Root) (string, error) {
	f, _, e := openWriteDir(r, nil)
	if e != nil {
		return "", e
	}
	defer f.Close()
	p, e := openNT(f, Private, true, windows.FILE_GENERIC_READ, share, windows.FILE_CREATE, true)
	if e != nil {
		return "", e
	}
	defer p.Close()
	if _, e = policy(p); e != nil {
		return "", e
	}
	return ObjectIdentity(p)
}

type WriteArea struct{ Parent, Private *os.File }

func DiskFull(e error) bool {
	return errors.Is(e, windows.ERROR_DISK_FULL) || errors.Is(e, windows.ERROR_HANDLE_DISK_FULL) || errors.Is(e, windows.STATUS_DISK_FULL)
}
func CheckSpace(a WriteArea, wanted int64) error {
	if wanted < 0 {
		return ErrUnsafe
	}
	path, e := finalName(windows.Handle(a.Private.Fd()), 1)
	if e != nil {
		return e
	}
	ptr, e := windows.UTF16PtrFromString(path)
	if e != nil {
		return e
	}
	var free, total uint64
	if e = windows.GetDiskFreeSpaceEx(ptr, &free, &total, nil); e != nil {
		return e
	}
	reserve := max(uint64(1<<30), total/20)
	if free < reserve || free-reserve < uint64(wanted) {
		return windows.ERROR_DISK_FULL
	}
	return nil
}

func (a WriteArea) Close() { a.Parent.Close(); a.Private.Close() }
func (r Root) WriteArea(parts []string, pin string) (WriteArea, error) {
	if pin == "" {
		return WriteArea{}, ErrUnsafe
	}
	base, _, e := openWriteDir(r, nil)
	if e != nil {
		return WriteArea{}, e
	}
	defer base.Close()
	p, e := openNT(base, Private, true, windows.FILE_GENERIC_READ, share, windows.FILE_OPEN, true)
	if e != nil {
		return WriteArea{}, e
	}
	id, e := ObjectIdentity(p)
	if e != nil || id != pin {
		p.Close()
		return WriteArea{}, ErrUnsafe
	}
	policyP, e := policy(p)
	if e != nil {
		p.Close()
		return WriteArea{}, e
	}
	d, policyD, e := openWriteDir(r, parts)
	if e != nil {
		p.Close()
		return WriteArea{}, e
	}
	if policyD != policyP {
		p.Close()
		d.Close()
		return WriteArea{}, ErrUnsafe
	}
	return WriteArea{d, p}, nil
}

func Absent(e error) bool {
	return errors.Is(e, windows.STATUS_OBJECT_NAME_NOT_FOUND) || errors.Is(e, windows.STATUS_NO_SUCH_FILE) || errors.Is(e, os.ErrNotExist)
}
func Collision(e error) bool {
	return errors.Is(e, windows.STATUS_OBJECT_NAME_COLLISION) || errors.Is(e, os.ErrExist)
}

func stageHandle(a WriteArea, id string, create bool) (*os.File, error) {
	if len(id) != 43 || strings.ContainsFunc(id, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	}) {
		return nil, ErrUnsafe
	}
	d := uint32(windows.FILE_OPEN)
	if create {
		d = windows.FILE_CREATE
	}
	f, e := openNT(a.Private, id, false, windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.DELETE, windows.FILE_SHARE_READ, d, false)
	if e != nil {
		return nil, e
	}
	if _, e = policy(f); e != nil {
		f.Close()
		return nil, e
	}
	var i windows.ByHandleFileInformation
	if e = windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &i); e != nil || i.NumberOfLinks != 1 || i.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		f.Close()
		return nil, ErrUnsafe
	}
	return f, nil
}
func (a WriteArea) Stage(id string, create bool) (*os.File, error) { return stageHandle(a, id, create) }

func OpenSource(r Root, parts []string) (*os.File, error) {
	if len(parts) < 1 || !Part(parts[len(parts)-1]) {
		return nil, ErrUnsafe
	}
	d, e := r.OpenDir(parts[:len(parts)-1])
	if e != nil {
		return nil, e
	}
	defer d.Close()
	f, e := openNT(d, parts[len(parts)-1], false, windows.FILE_GENERIC_READ, windows.FILE_SHARE_READ, windows.FILE_OPEN, false)
	if e != nil {
		return nil, e
	}
	i, e := f.Stat()
	if e != nil || !i.Mode().IsRegular() {
		f.Close()
		return nil, ErrUnsafe
	}
	return f, nil
}

func OpenFinal(a WriteArea, leaf string) (*os.File, error) {
	if !Part(leaf) {
		return nil, ErrUnsafe
	}
	return openNT(a.Parent, leaf, false, windows.FILE_GENERIC_READ, windows.FILE_SHARE_READ, windows.FILE_OPEN, false)
}

func SetMTime(f *os.File, t time.Time) error {
	v := windows.NsecToFiletime(t.UnixNano())
	return windows.SetFileTime(windows.Handle(f.Fd()), nil, nil, &v)
}

func Publish(f, to *os.File, name string) error {
	if !Part(name) {
		return ErrUnsafe
	}
	x, _, e := identify(f)
	if e != nil {
		return e
	}
	y, _, e := identify(to)
	if e != nil || x.volume != y.volume {
		return ErrUnsafe
	}
	type rename struct {
		Flags     uint32
		Root      windows.Handle
		NameBytes uint32
		Name      uint16
	}
	u, e := windows.UTF16FromString(name)
	if e != nil {
		return e
	}
	u = u[:len(u)-1]
	var h rename
	off := int(unsafe.Offsetof(h.Name))
	size := off + 2*len(u)
	buf := make([]byte, max(int(unsafe.Sizeof(h)), size))
	v := (*rename)(unsafe.Pointer(&buf[0]))
	v.Root = windows.Handle(to.Fd())
	v.NameBytes = uint32(len(u) * 2)
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(&buf[off])), len(u)), u)
	var st windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(windows.Handle(f.Fd()), &st, &buf[0], uint32(size), windows.FileRenameInformation)
}
