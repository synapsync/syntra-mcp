//go:build windows

package toolclient

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	windowsSharePinnedDirectory = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE
	windowsShareOrdinaryFile    = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE
	windowsFullFileAccess       = uint32(0x001F01FF)
)

// windowsStoreFS keeps the final directory, every traversed ancestor, and the
// process lock open for the lifetime of Store. All child operations are
// relative to dir; path names are never re-resolved after the initial walk.
type windowsStoreFS struct {
	dir       windows.Handle
	lock      windows.Handle
	ancestors []windows.Handle
	userSID   *windows.SID
	fsName    string
}

func (s *windowsStoreFS) close() error {
	if s == nil {
		return nil
	}
	var first error
	closeOne := func(handle *windows.Handle) {
		if *handle == 0 || *handle == windows.InvalidHandle {
			return
		}
		if err := windows.CloseHandle(*handle); err != nil && first == nil {
			first = err
		}
		*handle = windows.InvalidHandle
	}
	closeOne(&s.lock)
	closeOne(&s.dir)
	for i := len(s.ancestors) - 1; i >= 0; i-- {
		closeOne(&s.ancestors[i])
	}
	s.ancestors = nil
	return first
}

func (s *windowsStoreFS) checkDir() error {
	if s == nil || s.dir == 0 || s.dir == windows.InvalidHandle || s.userSID == nil {
		return ErrProfile
	}
	if checkPrivateDirectory(s.dir, s.userSID) != nil {
		return ErrProfile
	}
	fsName, flags, err := volumeInfoByHandle(s.dir)
	if err != nil || !supportedPrivateVolume(fsName, flags) || !strings.EqualFold(fsName, s.fsName) {
		return ErrProfile
	}
	return nil
}

func (s *windowsStoreFS) openRead(name string) (*os.File, error) {
	if s == nil || !validPrivateFilename(name) || s.checkDir() != nil {
		return nil, ErrProfile
	}
	handle, err := ntCreateRelative(
		s.dir, name,
		windows.FILE_READ_DATA|windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL|windows.SYNCHRONIZE,
		windowsShareOrdinaryFile, windows.FILE_OPEN,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		nil,
	)
	if isMissingWindowsObject(err) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, ErrProfile
	}
	if checkPrivateFile(handle, s.userSID) != nil || s.checkDir() != nil {
		_ = windows.CloseHandle(handle)
		return nil, ErrProfile
	}
	return os.NewFile(uintptr(handle), "private profile file"), nil
}

func (s *windowsStoreFS) createExclusive(name string) (*os.File, error) {
	if s == nil || !validPrivateFilename(name) || s.checkDir() != nil {
		return nil, ErrProfile
	}
	handle, err := ntCreateRelative(
		s.dir, name,
		windows.FILE_GENERIC_WRITE|windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL|windows.DELETE|windows.SYNCHRONIZE,
		windowsShareOrdinaryFile, windows.FILE_CREATE,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_WRITE_THROUGH,
		privateSecurityDescriptor(s.userSID),
	)
	if err != nil {
		return nil, ErrProfile
	}
	if checkPrivateFile(handle, s.userSID) != nil || s.checkDir() != nil {
		markHandleForDeletion(handle)
		_ = windows.CloseHandle(handle)
		return nil, ErrProfile
	}
	return os.NewFile(uintptr(handle), "temporary private profile file"), nil
}

func (s *windowsStoreFS) syncFile(file *os.File) error {
	if s == nil || file == nil || s.checkDir() != nil {
		return ErrProfile
	}
	handle := windows.Handle(file.Fd())
	if checkPrivateFile(handle, s.userSID) != nil {
		return ErrProfile
	}
	if err := windows.FlushFileBuffers(handle); err != nil {
		return ErrProfile
	}
	return nil
}

func (s *windowsStoreFS) syncDir() error {
	if s == nil || s.checkDir() != nil {
		return ErrProfile
	}
	// FlushFileBuffers on the pinned directory handle is the Windows metadata
	// durability boundary. The handle is opened with write access; failure is
	// propagated instead of pretending that a directory flush succeeded.
	if err := windows.FlushFileBuffers(s.dir); err != nil {
		return ErrProfile
	}
	return nil
}

func (s *windowsStoreFS) rename(oldName, newName string, replace bool) error {
	if s == nil || !validPrivateFilename(oldName) || !validPrivateFilename(newName) || s.checkDir() != nil {
		return ErrProfile
	}
	source, err := ntCreateRelative(
		s.dir, oldName,
		windows.DELETE|windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL|windows.SYNCHRONIZE,
		windowsShareOrdinaryFile, windows.FILE_OPEN,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_WRITE_THROUGH,
		nil,
	)
	if isMissingWindowsObject(err) {
		return os.ErrNotExist
	}
	if err != nil {
		return ErrProfile
	}
	defer windows.CloseHandle(source)
	if checkPrivateFile(source, s.userSID) != nil {
		return ErrProfile
	}

	// Validate any existing destination before replacement. The directory is
	// private and its handle is pinned; the rename itself remains atomic.
	target, targetErr := ntCreateRelative(
		s.dir, newName,
		windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL|windows.SYNCHRONIZE,
		windowsShareOrdinaryFile, windows.FILE_OPEN,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		nil,
	)
	if targetErr == nil {
		if checkPrivateFile(target, s.userSID) != nil {
			_ = windows.CloseHandle(target)
			return ErrProfile
		}
		_ = windows.CloseHandle(target)
	} else if !isMissingWindowsObject(targetErr) {
		return ErrProfile
	}
	if s.checkDir() != nil {
		return ErrProfile
	}

	name, err := windows.UTF16FromString(newName)
	if err != nil || len(name) < 1 {
		return ErrProfile
	}
	nameBytes := (len(name) - 1) * 2
	var layout windowsRenameInformation
	bufferSize := int(unsafe.Sizeof(layout)) + nameBytes
	buffer := make([]byte, bufferSize)
	info := (*windowsRenameInformation)(unsafe.Pointer(&buffer[0]))
	if replace {
		info.ReplaceIfExists = 1
	}
	// RootDirectory is NULL per FILE_RENAME_INFORMATION's documented form for
	// a simple basename renamed inside the same directory. The source was opened
	// relative to the pinned directory, so this cannot escape that directory:
	// https://learn.microsoft.com/en-us/windows-hardware/drivers/ddi/ntifs/ns-ntifs-_file_rename_information
	info.FileNameLength = uint32(nameBytes)
	copy(unsafe.Slice(&info.FileName[0], nameBytes/2), name[:len(name)-1])
	var ioStatus windows.IO_STATUS_BLOCK
	if err := windows.NtSetInformationFile(source, &ioStatus, &buffer[0], uint32(len(buffer)), windows.FileRenameInformation); err != nil {
		return ErrProfile
	}
	runtime.KeepAlive(buffer)
	return nil
}

func (s *windowsStoreFS) remove(name string) error {
	if s == nil || !validPrivateFilename(name) || s.checkDir() != nil {
		return ErrProfile
	}
	handle, err := ntCreateRelative(
		s.dir, name,
		windows.DELETE|windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL|windows.SYNCHRONIZE,
		windowsShareOrdinaryFile, windows.FILE_OPEN,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_WRITE_THROUGH,
		nil,
	)
	if isMissingWindowsObject(err) {
		return os.ErrNotExist
	}
	if err != nil {
		return ErrProfile
	}
	defer windows.CloseHandle(handle)
	if checkPrivateFile(handle, s.userSID) != nil || s.checkDir() != nil {
		return ErrProfile
	}
	if err = markHandleForDeletion(handle); err != nil {
		return ErrProfile
	}
	return nil
}

// OpenStore accepts only a canonical local drive path on a fixed volume with
// persistent ACLs. Each component is opened relative to its already validated
// parent with reparse processing disabled. Pinned directory handles omit
// FILE_SHARE_DELETE, preventing concurrent renames of checked ancestors.
func OpenStore(ctx context.Context, path string) (*Store, error) {
	if ctx.Err() != nil {
		return nil, errors.New("profile lock deadline or cancellation")
	}
	drive, parts, err := parseWindowsStorePath(path)
	if err != nil {
		return nil, ErrProfile
	}
	rootPath, _ := windows.UTF16PtrFromString(drive + `\`)
	if windows.GetDriveType(rootPath) != windows.DRIVE_FIXED {
		return nil, ErrProfile
	}
	userSID, err := currentWindowsUserSID()
	if err != nil {
		return nil, ErrProfile
	}
	security := privateSecurityDescriptor(userSID)
	if security == nil {
		return nil, ErrProfile
	}

	root, err := ntCreateRelative(
		0, `\??\`+drive+`\`,
		windows.FILE_LIST_DIRECTORY|windows.FILE_TRAVERSE|windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN,
		windows.FILE_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		nil,
	)
	if err != nil {
		return nil, ErrProfile
	}
	if err = checkDirectoryAttributes(root); err != nil {
		_ = windows.CloseHandle(root)
		return nil, ErrProfile
	}
	fsName, fsFlags, err := volumeInfoByHandle(root)
	if err != nil || !supportedPrivateVolume(fsName, fsFlags) {
		_ = windows.CloseHandle(root)
		return nil, ErrProfile
	}

	handles := []windows.Handle{root}
	cleanup := func() {
		for i := len(handles) - 1; i >= 0; i-- {
			_ = windows.CloseHandle(handles[i])
		}
	}
	for i, part := range parts {
		final := i == len(parts)-1
		access := uint32(windows.FILE_LIST_DIRECTORY | windows.FILE_TRAVERSE | windows.FILE_READ_ATTRIBUTES | windows.READ_CONTROL | windows.SYNCHRONIZE)
		options := uint32(windows.FILE_DIRECTORY_FILE | windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT)
		if final || i == len(parts)-2 {
			// Generic write is limited to the private store and its immediate
			// parent for required directory flushes. Earlier ancestors remain
			// read/traverse-only.
			access |= windows.FILE_GENERIC_WRITE
		}
		if final {
			options |= windows.FILE_WRITE_THROUGH
		}
		parent := handles[len(handles)-1]
		var child windows.Handle
		if final {
			child, err = ntCreateRelative(parent, part, access, windowsSharePinnedDirectory, windows.FILE_CREATE, options, security)
			if winStatus(err, windows.STATUS_OBJECT_NAME_COLLISION) {
				child, err = ntCreateRelative(parent, part, access, windowsSharePinnedDirectory, windows.FILE_OPEN, options&^windows.FILE_WRITE_THROUGH, nil)
			}
		} else {
			child, err = ntCreateRelative(parent, part, access, windowsSharePinnedDirectory, windows.FILE_OPEN, options, nil)
		}
		if err != nil {
			cleanup()
			return nil, ErrProfile
		}
		if checkDirectoryAttributes(child) != nil {
			_ = windows.CloseHandle(child)
			cleanup()
			return nil, ErrProfile
		}
		handles = append(handles, child)
	}

	dir := handles[len(handles)-1]
	parentDir := handles[len(handles)-2]
	// The final directory entry is stored in its parent. Flush that pinned
	// handle before creating the process lock or writing any profile data.
	if windows.FlushFileBuffers(parentDir) != nil {
		cleanup()
		return nil, ErrProfile
	}
	if checkPrivateDirectory(dir, userSID) != nil {
		cleanup()
		return nil, ErrProfile
	}
	finalFSName, finalFSFlags, err := volumeInfoByHandle(dir)
	if err != nil || !strings.EqualFold(finalFSName, fsName) || !supportedPrivateVolume(finalFSName, finalFSFlags) {
		cleanup()
		return nil, ErrProfile
	}
	fs := &windowsStoreFS{
		dir:       dir,
		lock:      windows.InvalidHandle,
		ancestors: append([]windows.Handle(nil), handles[:len(handles)-1]...),
		userSID:   userSID,
		fsName:    finalFSName,
	}
	// A directory flush failure is fatal. FILE_WRITE_THROUGH is also set on
	// creation, rename, and delete requests. Microsoft documents that NTFS
	// write-through requests flush resulting metadata changes, including rename:
	// https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-createfilea
	// Directory FlushFileBuffers is documented for persistent storage including
	// NTFS: https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-flushfilebuffers
	if fs.syncDir() != nil {
		_ = fs.close()
		return nil, ErrProfile
	}

	lockSecurity := privateSecurityDescriptor(userSID)
	for {
		if ctx.Err() != nil {
			_ = fs.close()
			return nil, errors.New("profile lock deadline or cancellation")
		}
		lock, lockErr := ntCreateRelative(
			dir, ".lock",
			windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL|windows.SYNCHRONIZE,
			0, windows.FILE_OPEN_IF,
			windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_WRITE_THROUGH,
			lockSecurity,
		)
		if lockErr == nil {
			if checkPrivateFile(lock, userSID) != nil || fs.checkDir() != nil {
				_ = windows.CloseHandle(lock)
				_ = fs.close()
				return nil, ErrProfile
			}
			if ctx.Err() != nil {
				_ = windows.CloseHandle(lock)
				_ = fs.close()
				return nil, errors.New("profile lock deadline or cancellation")
			}
			fs.lock = lock
			return &Store{fs: fs}, nil
		}
		if !winStatus(lockErr, windows.STATUS_SHARING_VIOLATION) {
			_ = fs.close()
			return nil, ErrProfile
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			_ = fs.close()
			return nil, errors.New("profile lock deadline or cancellation")
		case <-timer.C:
		}
	}
}

func parseWindowsStorePath(path string) (string, []string, error) {
	if path == "" || !utf8.ValidString(path) || filepath.Clean(path) != path || !filepath.IsAbs(path) {
		return "", nil, ErrProfile
	}
	volume := filepath.VolumeName(path)
	if len(volume) != 2 || volume[1] != ':' || !((volume[0] >= 'A' && volume[0] <= 'Z') || (volume[0] >= 'a' && volume[0] <= 'z')) {
		return "", nil, ErrProfile
	}
	if len(path) < 4 || path[2] != '\\' || strings.HasPrefix(path, `\\`) {
		return "", nil, ErrProfile
	}
	parts := strings.Split(path[3:], `\`)
	// A writable parent is required to flush the final directory entry during
	// creation and recovery, matching the Unix adapter's parent-sync boundary.
	if len(parts) < 2 {
		return "", nil, ErrProfile
	}
	for _, part := range parts {
		if !validWindowsComponent(part) {
			return "", nil, ErrProfile
		}
	}
	return strings.ToUpper(volume), parts, nil
}

func validWindowsComponent(part string) bool {
	if part == "" || part == "." || part == ".." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
		return false
	}
	for _, r := range part {
		if r < 0x20 || strings.ContainsRune(`<>:"/\\|?*`, r) {
			return false
		}
	}
	deviceBase := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
	switch deviceBase {
	case "CON", "PRN", "AUX", "NUL", "CLOCK$":
		return false
	}
	if len(deviceBase) == 4 && (strings.HasPrefix(deviceBase, "COM") || strings.HasPrefix(deviceBase, "LPT")) && deviceBase[3] >= '1' && deviceBase[3] <= '9' {
		return false
	}
	return true
}

func validPrivateFilename(name string) bool {
	return name != "" && filepath.Base(name) == name && name != "." && name != ".." && validWindowsComponent(name)
}

func currentWindowsUserSID() (*windows.SID, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return nil, err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil || !user.User.Sid.IsValid() {
		return nil, ErrProfile
	}
	return user.User.Sid, nil
}

func privateSecurityDescriptor(sid *windows.SID) *windows.SECURITY_DESCRIPTOR {
	if sid == nil || !sid.IsValid() {
		return nil
	}
	text := fmt.Sprintf("O:%sD:P(A;;FA;;;%s)", sid.String(), sid.String())
	sd, err := windows.SecurityDescriptorFromString(text)
	if err != nil || sd == nil {
		return nil
	}
	return sd
}

func checkPrivateDirectory(handle windows.Handle, sid *windows.SID) error {
	if sid == nil || checkDirectoryAttributes(handle) != nil || checkPrivateACL(handle, sid) != nil {
		return ErrProfile
	}
	return nil
}

func checkPrivateFile(handle windows.Handle, sid *windows.SID) error {
	if sid == nil || checkFileAttributes(handle) != nil || checkPrivateACL(handle, sid) != nil {
		return ErrProfile
	}
	return nil
}

func checkDirectoryAttributes(handle windows.Handle) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return ErrProfile
	}
	return nil
}

func checkFileAttributes(handle windows.Handle) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 || info.NumberOfLinks != 1 {
		return ErrProfile
	}
	return nil
}

func checkPrivateACL(handle windows.Handle, userSID *windows.SID) error {
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || sd == nil || !sd.IsValid() {
		return ErrProfile
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !windows.EqualSid(owner, userSID) {
		return ErrProfile
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PRESENT == 0 || control&windows.SE_DACL_PROTECTED == 0 {
		return ErrProfile
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 1 {
		return ErrProfile
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil || ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != 0 || uint32(ace.Mask) != windowsFullFileAccess {
		return ErrProfile
	}
	sidOffset := unsafe.Offsetof(windows.ACCESS_ALLOWED_ACE{}.SidStart)
	if uintptr(ace.Header.AceSize) < sidOffset+8 {
		return ErrProfile
	}
	aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !aceSID.IsValid() {
		return ErrProfile
	}
	sidLength := windows.GetLengthSid(aceSID)
	if uintptr(sidLength) > uintptr(ace.Header.AceSize)-sidOffset || sidOffset+uintptr(sidLength) != uintptr(ace.Header.AceSize) || !windows.EqualSid(aceSID, userSID) {
		return ErrProfile
	}
	return nil
}

func volumeInfoByHandle(handle windows.Handle) (string, uint32, error) {
	var volumeName [windows.MAX_PATH]uint16
	var fileSystemName [windows.MAX_PATH]uint16
	var serial, maximumComponentLength, flags uint32
	if err := windows.GetVolumeInformationByHandle(
		handle,
		&volumeName[0], uint32(len(volumeName)), &serial,
		&maximumComponentLength, &flags,
		&fileSystemName[0], uint32(len(fileSystemName)),
	); err != nil {
		return "", 0, err
	}
	return windows.UTF16ToString(fileSystemName[:]), flags, nil
}

func supportedPrivateVolume(name string, flags uint32) bool {
	return flags&windows.FILE_PERSISTENT_ACLS != 0 && strings.EqualFold(name, "NTFS")
}

func ntCreateRelative(root windows.Handle, name string, access, share, disposition, options uint32, security *windows.SECURITY_DESCRIPTOR) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return windows.InvalidHandle, err
	}
	oa := &windows.OBJECT_ATTRIBUTES{
		RootDirectory:      root,
		ObjectName:         objectName,
		Attributes:         windows.OBJ_CASE_INSENSITIVE,
		SecurityDescriptor: security,
	}
	if root != 0 {
		// The DOS drive root is an object-manager symbolic link, so the initial
		// absolute open must resolve it. All later names are relative and disable
		// object-manager reparsing in addition to filesystem reparse opens.
		oa.Attributes |= windows.OBJ_DONT_REPARSE
	}
	oa.Length = uint32(unsafe.Sizeof(*oa))
	var handle windows.Handle
	var ioStatus windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, access, oa, &ioStatus, nil, windows.FILE_ATTRIBUTE_NORMAL, share, disposition, options, 0, 0)
	runtime.KeepAlive(objectName)
	runtime.KeepAlive(security)
	runtime.KeepAlive(oa)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return handle, nil
}

type windowsRenameInformation struct {
	ReplaceIfExists uint32
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [1]uint16
}

func markHandleForDeletion(handle windows.Handle) error {
	info := struct{ DeleteFile uint8 }{DeleteFile: 1}
	var ioStatus windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(handle, &ioStatus, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), windows.FileDispositionInformation)
}

func winStatus(err error, status windows.NTStatus) bool {
	got, ok := err.(windows.NTStatus)
	return ok && got == status
}

func isMissingWindowsObject(err error) bool {
	return winStatus(err, windows.STATUS_OBJECT_NAME_NOT_FOUND) || winStatus(err, windows.STATUS_OBJECT_PATH_NOT_FOUND)
}
