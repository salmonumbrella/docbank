//go:build !linux && !darwin && !windows

package ingest

import (
	"errors"
	"io/fs"
	"os"
)

func fileChangeTime(_ *os.File, _ fs.FileInfo) (fileChangeStamp, error) {
	return fileChangeStamp{}, errors.New("native source change observation is unsupported on this platform")
}
