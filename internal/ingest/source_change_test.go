package ingest

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/docbank/internal/blob"
)

func observedNativeFingerprint(t *testing.T, path string) localFileFingerprint {
	t.Helper()
	fingerprint, err := observeLocalFileFingerprintWithChange(func() (*os.File, error) {
		return blob.OpenNoFollow(path)
	}, true)
	require.NoError(t, err)
	require.True(t, fingerprint.change.present)
	return fingerprint
}

// Real filesystem clocks are separate from the fake watcher settling clock.
// Retry an ordinary operation until this filesystem exposes its change stamp.
// Call outside testing/synctest, where Sleep would not advance the native clock.
func changeSourceWithRestoredMtime(t *testing.T, path string, operation func()) {
	t.Helper()
	before := observedNativeFingerprint(t, path)
	deadline := time.Now().Add(3 * time.Second)
	for {
		operation()
		require.NoError(t, os.Chtimes(path, before.info.ModTime(), before.info.ModTime()))
		after := observedNativeFingerprint(t, path)
		require.Equal(t, before.size, after.size)
		require.Equal(t, before.modTime, after.modTime)
		require.True(t, os.SameFile(before.info, after.info))
		if before.change != after.change {
			return
		}
		require.True(t, time.Now().Before(deadline), "native stamp did not advance: before=%+v after=%+v", before.change, after.change)
		time.Sleep(10 * time.Millisecond) //nolint:kennlint // native filesystem clock must advance outside synctest
	}
}

func rewriteSourceWithRestoredMtime(t *testing.T, path string, content []byte) {
	t.Helper()
	changeSourceWithRestoredMtime(t, path, func() {
		require.NoError(t, os.WriteFile(path, content, 0o600))
	})
}

func TestSourceChangeTimeOrdinaryWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source")
	require.NoError(t, os.WriteFile(path, []byte("alpha"), 0o600))
	before := observedNativeFingerprint(t, path)
	base := fingerprintFileInfo(before.info)
	require.False(t, before.matches(base), "required evidence cannot be omitted")
	require.False(t, base.matches(before))
	rewriteSourceWithRestoredMtime(t, path, []byte("bravo"))
	after := observedNativeFingerprint(t, path)
	require.False(t, before.matches(after))
	require.True(t, base.matches(fingerprintFileInfo(after.info)))
	require.True(t, after.matches(observedNativeFingerprint(t, path)))
}
