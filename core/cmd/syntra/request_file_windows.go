//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

func openRequestFile(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_SEQUENTIAL_SCAN, 0)
	if err != nil {
		return nil, err
	}
	fileType, err := windows.GetFileType(h)
	if err != nil || fileType != windows.FILE_TYPE_DISK {
		_ = windows.CloseHandle(h)
		if err != nil {
			return nil, err
		}
		return nil, windows.ERROR_INVALID_PARAMETER
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		_ = windows.CloseHandle(h)
		return nil, err
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		_ = windows.CloseHandle(h)
		return nil, windows.ERROR_INVALID_PARAMETER
	}
	file := os.NewFile(uintptr(h), "request file")
	if file == nil {
		_ = windows.CloseHandle(h)
		return nil, windows.ERROR_INVALID_HANDLE
	}
	return file, nil
}
