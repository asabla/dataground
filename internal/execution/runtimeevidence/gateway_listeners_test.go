package runtimeevidence

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const socketHeader = "  sl  local_address rem_address st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode\n"

func gatewaySocketRow(address, port, inode string) string {
	return fmt.Sprintf("  0: %s:%s 00000000:0000 0A 00000000:00000000 00:00000000 00000000 1000 0 %s 1 0000000000000000 100 0 0 10 0\n", address, port, inode)
}
func gatewayLoopbackHex() string {
	var value [4]byte
	binary.NativeEndian.PutUint32(value[:], 0x7f000001)
	return strings.ToUpper(hex.EncodeToString(value[:]))
}
func TestGatewayListenersRequireBothOwnedLoopbackSockets(t *testing.T) {
	valid := socketHeader + gatewaySocketRow(gatewayLoopbackHex(), "1F90", "123") + gatewaySocketRow(gatewayLoopbackHex(), "1F91", "456")
	owned := map[string]bool{"123": true, "456": true}
	for name, mutate := range map[string]func(string) string{
		"wildcard":           func(s string) string { return strings.Replace(s, gatewayLoopbackHex(), "00000000", 1) },
		"other owner":        func(s string) string { return strings.Replace(s, " 123 ", " 789 ", 1) },
		"duplicate listener": func(s string) string { return s + gatewaySocketRow(gatewayLoopbackHex(), "1F90", "123") },
		"invalid address":    func(s string) string { return strings.Replace(s, gatewayLoopbackHex(), "invalid", 1) },
		"short address":      func(s string) string { return strings.Replace(s, gatewayLoopbackHex(), "0100", 1) },
		"malformed row":      func(s string) string { return s + "0: invalid\n" },
		"header missing":     func(s string) string { return strings.TrimPrefix(s, socketHeader) },
	} {
		t.Run(name, func(t *testing.T) {
			if collectGatewayListeners([]byte(mutate(valid)), false, owned, map[string]string{}, testBridgeAddress()) == nil {
				t.Fatal("invalid listener observation accepted")
			}
		})
	}
	listeners := map[string]string{}
	if err := collectGatewayListeners([]byte(valid+gatewaySocketRow("00000000", "ABCD", "789")), false, owned, listeners, testBridgeAddress()); err != nil || len(listeners) != 2 {
		t.Fatal("valid listeners with separate driver socket rejected")
	}
	for _, address := range []string{strings.Repeat("0", 32), strings.Repeat("0", 24) + "01000000"} {
		if collectGatewayListeners([]byte(socketHeader+gatewaySocketRow(address, "1F90", "123")), true, owned, map[string]string{}, testBridgeAddress()) == nil {
			t.Fatal("IPv6 control listener accepted")
		}
	}
}

func gatewayProcFixture(t *testing.T) (string, *os.Root) {
	t.Helper()
	path := t.TempDir()
	for _, name := range []string{"fd", "net"} {
		if err := os.Mkdir(filepath.Join(path, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"stat":     "123 (gateway (name)) S " + strings.Repeat("0 ", 18) + "12345 0\n",
		"net/tcp":  socketHeader + gatewaySocketRow(gatewayLoopbackHex(), "1F90", "123") + gatewaySocketRow(gatewayLoopbackHex(), "1F91", "456"),
		"net/tcp6": socketHeader,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(path, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range map[string]string{"1": "/dev/null", "3": "socket:[123]", "4": "socket:[456]"} {
		if err := os.Symlink(target, filepath.Join(path, "fd", name)); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return path, root
}
func TestGatewayProcObservationFailsClosed(t *testing.T) {
	_, root := gatewayProcFixture(t)
	if err := checkGatewayProcListeners(context.Background(), root, testBridgeAddress()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(string) error{
		"missing control owner": func(p string) error { return os.Remove(filepath.Join(p, "fd/3")) },
		"missing health owner":  func(p string) error { return os.Remove(filepath.Join(p, "fd/4")) },
		"missing table":         func(p string) error { return os.Remove(filepath.Join(p, "net/tcp6")) },
		"missing listener": func(p string) error {
			return os.WriteFile(filepath.Join(p, "net/tcp"), []byte(socketHeader+gatewaySocketRow(gatewayLoopbackHex(), "1F90", "123")), 0o600)
		},
		"oversized table": func(p string) error {
			return os.WriteFile(filepath.Join(p, "net/tcp"), []byte(strings.Repeat(" ", maximumGatewaySocketTable+1)), 0o600)
		},
		"malformed stat": func(p string) error { return os.WriteFile(filepath.Join(p, "stat"), []byte("123 (invalid) S"), 0o600) },
		"dead process": func(p string) error {
			return os.WriteFile(filepath.Join(p, "stat"), []byte("123 (gateway) Z "+strings.Repeat("0 ", 18)+"12345"), 0o600)
		},
		"missing stat": func(p string) error { return os.Remove(filepath.Join(p, "stat")) },
	} {
		t.Run(name, func(t *testing.T) {
			path, root := gatewayProcFixture(t)
			if err := mutate(path); err != nil {
				t.Fatal(err)
			}
			if err := checkGatewayProcListeners(context.Background(), root, testBridgeAddress()); err != ErrDockerTopologyDrift {
				t.Fatalf("unsafe observation: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if checkGatewayProcListeners(ctx, root, testBridgeAddress()) == nil {
		t.Fatal("cancellation accepted")
	}
}

func TestTopologyListenerFailureRetainsCleanupAuthority(t *testing.T) {
	id := strings.Repeat("a", 64)
	runner := &fakeDockerTopologyRunner{results: []dockerTopologyResult{{}, {output: id}, {output: runtimeTopologyInspection(testRunID, id)}, {}, {}, {}}}
	topology, _ := newTestDockerTopology(t, runner, func(context.Context) error { return nil })
	topology.state.checkListeners = func(_ context.Context, pid int, _ string) error {
		if pid != 123 {
			t.Fatal("listener observation used a different process")
		}
		return ErrDockerTopologyDrift
	}
	if !errors.Is(topology.Start(context.Background()), ErrDockerTopologyStart) {
		t.Fatal("listener failure accepted")
	}
	if err := topology.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
}

const testGatewayBridge = `{"name":"openshell-docker","driver":"bridge","scope":"local","ipamDriver":"default","config":[{"subnet":"172.17.0.0/16","gateway":"172.17.0.1"}]}`

func testBridgeAddress() string {
	address, _ := decodeGatewayBridge([]byte(testGatewayBridge))
	return address
}
func TestGatewayDriverRequiresExactPrivateBridgeAddress(t *testing.T) {
	for _, input := range []string{"{}", strings.Replace(testGatewayBridge, `"scope":"local"`, `"scope":"local","unexpected":true`, 1), strings.Replace(testGatewayBridge, `"local"`, `"swarm"`, 1), strings.ReplaceAll(testGatewayBridge, "172.17.", "8.8."), strings.Replace(testGatewayBridge, "172.17.0.1", "172.18.0.1", 1), strings.Replace(testGatewayBridge, `"default"`, `"other"`, 1)} {
		if _, err := decodeGatewayBridge([]byte(input)); err == nil {
			t.Fatal("invalid bridge accepted")
		}
	}
	loopback := socketHeader + gatewaySocketRow(gatewayLoopbackHex(), "1F90", "123") + gatewaySocketRow(gatewayLoopbackHex(), "1F91", "456")
	owned := map[string]bool{"123": true, "456": true, "789": true}
	for _, port := range []string{"1F90", "1F91"} {
		listeners := map[string]string{}
		err := collectGatewayListeners([]byte(loopback+gatewaySocketRow(testBridgeAddress(), port, "789")), false, owned, listeners, testBridgeAddress())
		if (err == nil) != (port == "1F90") {
			t.Fatal("incorrect bridge listener decision")
		}
	}
}
