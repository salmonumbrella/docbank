//go:build linux || darwin

package ingest

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

type sourceChangeInfo struct {
	fs.FileInfo

	native any
}

func (info sourceChangeInfo) Sys() any { return info.native }

func TestSourceChangeTimeRejectsMissingNativeInfo(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "source"))
	require.NoError(t, err)
	defer func() { require.NoError(t, file.Close()) }()
	info, err := file.Stat()
	require.NoError(t, err)
	for _, native := range []any{nil, "wrong type", (*syscall.Stat_t)(nil)} {
		stamp, err := fileChangeTime(file, sourceChangeInfo{FileInfo: info, native: native})
		require.ErrorContains(t, err, "native source change time")
		require.False(t, stamp.present)
		_, err = fingerprintOpenedFile(file, sourceChangeInfo{FileInfo: info, native: native}, true)
		require.Error(t, err)
		base, err := fingerprintOpenedFile(file, sourceChangeInfo{FileInfo: info, native: native}, false)
		require.NoError(t, err)
		require.False(t, base.change.present)
	}
}
