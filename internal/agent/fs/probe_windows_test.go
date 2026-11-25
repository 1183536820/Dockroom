package fs

import (
	"golang.org/x/sys/windows"
	"os"
	"unsafe"
)

func moveExclusive(from *os.File, old string, to *os.File, name string) error {
	if !Part(old) || !Part(name) {
		return ErrUnsafe
	}
	f, err := open(from, old, false, true, false)
	if err != nil {
		return err
	}
	defer f.Close()
	// FILE_RENAME_INFORMATION, ReplaceIfExists = FALSE, relative to destination handle.
	type rename struct {
		Flags     uint32
		Root      windows.Handle
		NameBytes uint32
		Name      uint16
	}
	u, err := windows.UTF16FromString(name)
	if err != nil {
		return err
	}
	u = u[:len(u)-1]
	var header rename
	off := int(unsafe.Offsetof(header.Name))
	size := off + 2*len(u)
	buf := make([]byte, max(int(unsafe.Sizeof(header)), size))
	ptr := (*rename)(unsafe.Pointer(&buf[0]))
	ptr.Root = windows.Handle(to.Fd())
	ptr.NameBytes = uint32(len(u) * 2)
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(&buf[off])), len(u)), u)
	var status windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(windows.Handle(f.Fd()), &status, &buf[0], uint32(size), windows.FileRenameInformation)
}
