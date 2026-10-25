package ingest

import (
	"io/fs"
	"os"
)

// fileChangeStamp is native change-time evidence, not a content identity.
// It is private to a live observation and is never persisted.
type fileChangeStamp struct {
	seconds     int64
	nanoseconds int64
	present     bool
}

func fingerprintOpenedFile(file *os.File, info fs.FileInfo, withChange bool) (localFileFingerprint, error) {
	fingerprint := fingerprintFileInfo(info)
	if withChange {
		stamp, err := fileChangeTime(file, info)
		if err != nil {
			return localFileFingerprint{}, err
		}
		fingerprint.change = stamp
	}
	return fingerprint, nil
}
