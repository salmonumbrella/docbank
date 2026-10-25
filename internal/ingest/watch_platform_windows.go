//go:build windows

package ingest

import (
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"io/fs"
	"os"
)

type watchMount uint32

func watchMetadataForFile(file *os.File, _ fs.FileInfo) (watchMount, watchFileIdentity, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return 0, watchFileIdentity{}, fmt.Errorf("reading %s file information: %w", file.Name(), err)
	}
	mount := watchMount(info.VolumeSerialNumber)
	ticks := uint64(info.CreationTime.HighDateTime)<<32 | uint64(info.CreationTime.LowDateTime)
	identity := watchFileIdentity{
		object: watchObjectKey{mount: mount, file: uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)},
		birth:  watchBirthFromFiletimeTicks(ticks),
	}
	return mount, identity, nil
}
func watchMountForFile(file *os.File) (watchMount, error) {
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	mount, _, err := watchMetadataForFile(file, info)
	return mount, err
}
func (mount watchMount) same(other watchMount) bool { return mount == other }
func transientWatchObservationError(err error) bool {
	return errors.Is(err, fs.ErrNotExist) ||
		errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
