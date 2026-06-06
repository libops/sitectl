package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func rpcResponseJSON(t *testing.T, result any) string {
	t.Helper()

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal(result) error = %v", err)
	}
	return rpcResponseEnvelopeJSON(t, RPCResponse{
		ProtocolVersion: RPCProtocolVersion,
		OK:              true,
		Result:          data,
	})
}

func rpcOutputResponseJSON(t *testing.T, output string) string {
	t.Helper()

	return rpcResponseEnvelopeJSON(t, RPCResponse{
		ProtocolVersion: RPCProtocolVersion,
		OK:              true,
		Output:          output,
	})
}

func rpcResponseEnvelopeJSON(t *testing.T, resp RPCResponse) string {
	t.Helper()

	out, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal(RPCResponse) error = %v", err)
	}
	return string(out)
}

func writeRPCFixturePlugin(t *testing.T, dir, name string, result any, fallback string) {
	t.Helper()

	writeRPCResponseFixturePlugin(t, dir, name, rpcResponseJSON(t, result), rpcFixtureEnv{
		fallback: fallback,
	})
}

func writeRPCOutputFixturePlugin(t *testing.T, dir, name, output, stderr string) {
	t.Helper()

	writeRPCResponseFixturePlugin(t, dir, name, rpcOutputResponseJSON(t, output), rpcFixtureEnv{
		stderr: stderr,
	})
}

type rpcFixtureEnv struct {
	fallback string
	stderr   string
}

func writeRPCResponseFixturePlugin(t *testing.T, dir, name, response string, env rpcFixtureEnv) {
	t.Helper()

	t.Setenv("SITECTL_TEST_RPC_RESPONSE", response)
	t.Setenv("SITECTL_TEST_PLUGIN_FALLBACK", env.fallback)
	t.Setenv("SITECTL_TEST_PLUGIN_STDERR", env.stderr)
	writePluginFixture(t, dir, name, "rpc-static-response.sh")
}

func writePluginFixture(t *testing.T, dir, name, fixture string) {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", fixture, err)
	}
	writePluginScript(t, dir, name, string(data))
}

func writePluginScript(t *testing.T, dir, name, script string) {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", name, err)
	}
}
