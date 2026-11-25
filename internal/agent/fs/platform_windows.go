package fs

import (
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func platformPart(s string) bool {
	if strings.ContainsAny(s, "<>\"|?*") || strings.HasSuffix(s, ".") || strings.HasSuffix(s, " ") {
		return false
	}
	for _, c := range s {
		if c < 32 {
			return false
		}
	}
	b, _, _ := strings.Cut(s, ".")
	b = strings.ToUpper(strings.TrimRight(b, " "))
	switch b {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return false
	}
	return !(strings.HasPrefix(b, "COM") || strings.HasPrefix(b, "LPT")) || !strings.Contains("123456789¹²³", b[3:])
}

const share = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE

func start(base string) (*os.File, error) {
	if len(base) != 3 || base[1] != ':' {
		return nil, ErrUnsafe
	}
	p, err := windows.UTF16PtrFromString(base)
	if err != nil {
		return nil, err
	}
	if windows.GetDriveType(p) != windows.DRIVE_FIXED {
		return nil, ErrUnsafe
	}
	h, err := windows.CreateFile(p, windows.FILE_GENERIC_READ, share, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	var name [32]uint16
	if err = windows.GetVolumeInformationByHandle(h, nil, 0, nil, nil, nil, &name[0], uint32(len(name))); err != nil || windows.UTF16ToString(name[:]) != "NTFS" {
		windows.CloseHandle(h)
		return nil, ErrUnsafe
	}
	f, err := checked(h, base, true, "")
	if err != nil {
		return nil, err
	}
	path, err := finalName(h, 1) // VOLUME_NAME_GUID, normalized
	if err != nil || !strings.HasPrefix(path, `\\?\Volume{`) || !strings.HasSuffix(path, `}\`) {
		f.Close()
		return nil, ErrUnsafe
	}
	return f, nil
}

func finalName(h windows.Handle, flags uint32) (string, error) {
	var buf [512]uint16
	n, e := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), flags)
	if e != nil {
		return "", e
	}
	if n >= uint32(len(buf)) {
		if n > 32768 {
			return "", ErrUnsafe
		}
		b := make([]uint16, n)
		n, e = windows.GetFinalPathNameByHandle(h, &b[0], uint32(len(b)), flags)
		if e != nil || n >= uint32(len(b)) {
			return "", ErrUnsafe
		}
		return windows.UTF16ToString(b[:n]), nil
	}
	return windows.UTF16ToString(buf[:n]), nil
}

func checked(h windows.Handle, name string, wantDir bool, volume string) (*os.File, error) {
	var info windows.ByHandleFileInformation
	err := windows.GetFileInformationByHandle(h, &info)
	typ, typeErr := windows.GetFileType(h)
	if err != nil || typeErr != nil || typ != windows.FILE_TYPE_DISK || info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DEVICE) != 0 || (wantDir && info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0) || (volume != "" && volume != fmt.Sprintf("%08x", info.VolumeSerialNumber)) {
		windows.CloseHandle(h)
		return nil, ErrUnsafe
	}
	return os.NewFile(uintptr(h), name), nil
}

func open(parent *os.File, name string, dir, deletion, _ bool) (*os.File, error) {
	access := uint32(windows.FILE_GENERIC_READ)
	if deletion {
		access |= windows.DELETE
	}
	return openNT(parent, name, dir, access, share, windows.FILE_OPEN, false)
}

func openNT(parent *os.File, name string, dir bool, access, shareMode, disposition uint32, internal bool) (*os.File, error) {
	n, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	var oa windows.OBJECT_ATTRIBUTES
	oa.Length = uint32(unsafe.Sizeof(oa))
	oa.RootDirectory = windows.Handle(parent.Fd())
	oa.ObjectName = n
	oa.Attributes = windows.OBJ_CASE_INSENSITIVE | 0x1000 // OBJ_DONT_REPARSE
	var h windows.Handle
	var io windows.IO_STATUS_BLOCK
	opts := uint32(windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT)
	if dir {
		opts |= windows.FILE_DIRECTORY_FILE
	}
	attr := uint32(0)
	if name == Private && disposition == windows.FILE_CREATE {
		attr = windows.FILE_ATTRIBUTE_HIDDEN
	}
	if err := windows.NtCreateFile(&h, access, &oa, &io, nil, attr, shareMode, disposition, opts, 0, 0); err != nil {
		return nil, err
	}
	id, _, err := identify(parent)
	if err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	f, err := checked(h, name, dir, id.volume)
	if err != nil {
		return nil, err
	}
	path, err := finalName(h, 1)
	leaf := path[strings.LastIndex(path, `\`)+1:]
	if err != nil || !strings.EqualFold(leaf, name) || (!Part(leaf) && !(internal && strings.EqualFold(leaf, Private))) {
		f.Close()
		return nil, ErrUnsafe
	}
	return f, nil
}

func identify(f *os.File) (identity, string, error) {
	var info windows.ByHandleFileInformation
	h := windows.Handle(f.Fd())
	err := windows.GetFileInformationByHandle(h, &info)
	if err != nil {
		return identity{}, "", err
	}
	var basic struct {
		Creation, Access, Write, Change int64
		Attr                            uint32
	}
	err = windows.GetFileInformationByHandleEx(h, windows.FileBasicInfo, (*byte)(unsafe.Pointer(&basic)), uint32(unsafe.Sizeof(basic)))
	return identity{fmt.Sprintf("%08x", info.VolumeSerialNumber), fmt.Sprintf("%08x%08x", info.FileIndexHigh, info.FileIndexLow)}, fmt.Sprint(basic.Change), err
}
