package statefile

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWriterClosedCannotPublishOrDeleteSuccessor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writer, err := NewWriter("ssh://owned", time.Now())
	require.NoError(t, err)
	require.NoError(t, writer.Write(nil, nil))
	require.NoError(t, writer.Close())
	require.NoError(t, os.WriteFile(writer.path, []byte("successor"), 0600))
	require.Error(t, writer.Write(nil, nil), "closed writer must not publish late")
	require.NoError(t, writer.Close())
	require.NoError(t, writer.Delete())
	contents, err := os.ReadFile(writer.path)
	require.NoError(t, err)
	require.Equal(t, "successor", string(contents))
}
