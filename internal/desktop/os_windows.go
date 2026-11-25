package desktop

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"runtime"
	"unsafe"
)

var shell = windows.NewLazySystemDLL("shell32.dll")
var com = windows.NewLazySystemDLL("ole32.dll")
var user32 = windows.NewLazySystemDLL("user32.dll")

func ShowError(e error) {
	msg, _ := windows.UTF16PtrFromString(e.Error())
	title, _ := windows.UTF16PtrFromString("渡柜")
	user32.NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(msg)), uintptr(unsafe.Pointer(title)), 0x10)
}
func openBrowser(url string) error {
	op, _ := windows.UTF16PtrFromString("open")
	s, _ := windows.UTF16PtrFromString(url)
	r, _, _ := shell.NewProc("ShellExecuteW").Call(0, uintptr(unsafe.Pointer(op)), uintptr(unsafe.Pointer(s)), 0, 0, 1)
	if r <= 32 {
		return errors.New("无法打开默认浏览器，请确认 Windows 已设置默认浏览器")
	}
	return nil
}
func folderPicker() (string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := com.NewProc("CoInitializeEx").Call(0, 2)
	if int32(hr) < 0 {
		return "", errors.New("无法打开 Windows 文件夹选择器")
	}
	defer com.NewProc("CoUninitialize").Call()
	var info struct {
		Owner    uintptr
		Root     uintptr
		Display  *uint16
		Title    *uint16
		Flags    uint32
		Callback uintptr
		Param    uintptr
		Image    int32
	}
	var display [windows.MAX_PATH]uint16
	info.Display = &display[0]
	info.Title, _ = windows.UTF16PtrFromString("选择要在渡柜中开放的文件夹（只读）")
	info.Flags = 0x0001 | 0x0040
	id, _, _ := shell.NewProc("SHBrowseForFolderW").Call(uintptr(unsafe.Pointer(&info)))
	if id == 0 {
		return "", nil
	}
	defer com.NewProc("CoTaskMemFree").Call(id)
	var path [windows.MAX_PATH]uint16
	ok, _, _ := shell.NewProc("SHGetPathFromIDListW").Call(id, uintptr(unsafe.Pointer(&path[0])))
	if ok == 0 {
		return "", errors.New("请选择本地 NTFS 上的普通文件夹")
	}
	return windows.UTF16ToString(path[:]), nil
}

func securedDir(path string, existing bool) error {
	user, e := windows.GetCurrentProcessToken().GetTokenUser()
	if e != nil {
		return e
	}
	sid := user.User.Sid.String()
	sd, e := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + sid + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if e != nil {
		return e
	}
	p, e := windows.UTF16PtrFromString(path)
	if e != nil {
		return e
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	e = windows.CreateDirectory(p, &sa)
	if e == nil || !existing || !errors.Is(e, windows.ERROR_ALREADY_EXISTS) {
		return e
	}
	f, e := windows.CreateFile(p, windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if e != nil {
		return e
	}
	defer windows.CloseHandle(f)
	var info windows.ByHandleFileInformation
	if e = windows.GetFileInformationByHandle(f, &info); e != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return errors.New("本机状态目录无效")
	}
	current, e := windows.GetSecurityInfo(f, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if e != nil {
		return e
	}
	owner, _, e := current.Owner()
	if e != nil || !owner.Equals(user.User.Sid) && owner.String() != "S-1-5-32-544" {
		return errors.New("本机状态目录所有者无效")
	}
	dacl, _, e := current.DACL()
	if e != nil || dacl == nil || dacl.AceCount == 0 {
		return errors.New("本机状态目录权限不安全")
	}
	for idx := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if e = windows.GetAce(dacl, idx, &ace); e != nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceSize < 16 {
			return errors.New("本机状态目录权限不安全")
		}
		s := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if s != sid && s != "S-1-5-18" && s != "S-1-5-32-544" {
			return errors.New("本机状态目录权限不安全")
		}
	}
	return nil
}

func lock(path string) (*os.File, error) {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{}); e != nil {
		f.Close()
		return nil, errors.New("渡柜已在运行。请使用已打开的本机管理页")
	}
	return f, nil
}
