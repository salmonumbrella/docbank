package ingest

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/require"
)

func TestSourceChangeWindowsBasicInfoABIAndInvalidHandle(t *testing.T) {
	require.Equal(t, uintptr(40), unsafe.Sizeof(fileBasicInfo{}))
	require.Equal(t, uintptr(24), unsafe.Offsetof(fileBasicInfo{}.changeTime))
	require.Equal(t, uintptr(32), unsafe.Offsetof(fileBasicInfo{}.fileAttributes))
	file, err := os.Create(filepath.Join(t.TempDir(), "source"))
	require.NoError(t, err)
	info, err := file.Stat()
	require.NoError(t, err)
	require.NoError(t, file.Close())
	stamp, err := fileChangeTime(file, info)
	require.ErrorContains(t, err, "native source change time")
	require.False(t, stamp.present)
}
