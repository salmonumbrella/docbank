//go:build darwin

package ingest

import "syscall"

func unixWatchBirth(stat *syscall.Stat_t) fileChangeStamp {
	return validWatchBirth(stat.Birthtimespec.Sec, stat.Birthtimespec.Nsec)
}
