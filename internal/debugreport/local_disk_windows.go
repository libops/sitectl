//go:build windows

package debugreport

import (
	"golang.org/x/sys/windows"
)

func localAvailableDiskBytes(path string) (int64, error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var freeBytesAvailable uint64
	if err := windows.GetDiskFreeSpaceEx(pathPtr, &freeBytesAvailable, nil, nil); err != nil {
		return 0, err
	}
	return int64(freeBytesAvailable), nil
}
