package jsonrpc

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParamsLogMeta_DoesNotLeakControlChars(t *testing.T) {
	t.Parallel()

	raw := []byte("{\"privateKey\":\"aabb\",\"x\":\"y\"}\n\r\t")
	n, preview := paramsLogMeta(raw)
	require.Equal(t, len(raw), n)
	require.NotContains(t, preview, "\n")
	require.NotContains(t, preview, "\r")
}

func TestParamsLogMeta_RedactsLongHex(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"privateKey":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}`)
	_, preview := paramsLogMeta(raw)
	require.Contains(t, preview, "[REDACTED]")
	require.NotContains(t, preview, "0123456789abcdef0123456789abcdef")
}

func TestEnableDPoSAdminRPC_DefaultOff(t *testing.T) {
	t.Setenv("VCITY_ENABLE_DPOS_ADMIN_RPC", "")
	require.False(t, enableDPoSAdminRPC())

	t.Setenv("VCITY_ENABLE_DPOS_ADMIN_RPC", "1")
	require.True(t, enableDPoSAdminRPC())

	t.Setenv("VCITY_ENABLE_DPOS_ADMIN_RPC", "true")
	require.True(t, enableDPoSAdminRPC())

	_ = os.Unsetenv("VCITY_ENABLE_DPOS_ADMIN_RPC")
}

func TestSanitizeRPCErrorMessage(t *testing.T) {
	t.Parallel()
	msg := sanitizeRPCErrorMessage("bad key aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899\nand more")
	require.NotContains(t, msg, "\n")
	require.Contains(t, msg, "[REDACTED]")
}

func TestParseJSONRPCAPIList(t *testing.T) {
	t.Parallel()
	m := parseJSONRPCAPIList([]string{"eth", " DEBUG ", ""})
	_, ok := m["eth"]
	require.True(t, ok)
	_, ok = m["debug"]
	require.True(t, ok)
	_, ok = m[""]
	require.False(t, ok)
}
