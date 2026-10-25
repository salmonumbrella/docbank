package ingest

import (
	"fmt"
	"io/fs"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// fileBasicInfo matches the 40-byte FILE_BASIC_INFO ABI, including trailing
// alignment on both supported Windows architectures.
type fileBasicInfo struct {
	creationTime   int64
	lastAccessTime int64
	lastWriteTime  int64
	changeTime     int64
	fileAttributes uint32
	padding        uint32
}

func fileChangeTime(file *os.File, _ fs.FileInfo) (fileChangeStamp, error) {
	var basic fileBasicInfo
	if err := windows.GetFileInformationByHandleEx(windows.Handle(file.Fd()), windows.FileBasicInfo,
		// #nosec G103 -- basic is a 40-byte, 8-byte-aligned FILE_BASIC_INFO buffer, live throughout this synchronous call.
		(*byte)(unsafe.Pointer(&basic)), uint32(unsafe.Sizeof(basic))); err != nil {
		return fileChangeStamp{}, fmt.Errorf("native source change time: %w", err)
	}
	if basic.changeTime <= 0 {
		return fileChangeStamp{}, fmt.Errorf("native source change time: unusable Windows ChangeTime %d", basic.changeTime)
	}
	const ticksPerSecond = 10_000_000
	return fileChangeStamp{seconds: basic.changeTime / ticksPerSecond,
		nanoseconds: (basic.changeTime % ticksPerSecond) * 100, present: true}, nil
}
