package ingest

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

func fileChangeTime(_ *os.File, info fs.FileInfo) (fileChangeStamp, error) {
	native, ok := info.Sys().(*syscall.Stat_t)
	if !ok || native == nil {
		return fileChangeStamp{}, errors.New("native source change time: missing linux stat data")
	}
	return fileChangeStamp{seconds: native.Ctim.Sec, nanoseconds: native.Ctim.Nsec, present: true}, nil
}
