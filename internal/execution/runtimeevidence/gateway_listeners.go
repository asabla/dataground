package runtimeevidence

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/netip"
	"os"
	"runtime"
	"strconv"
	"strings"
)

const maximumGatewaySocketTable = 4 << 20

// Docker identifies the gateway process. Linux supplies its socket ownership
// and local addresses; an unrelated health server cannot satisfy this check.
func checkGatewayProcessListeners(ctx context.Context, pid int, bridgeAddress string) error {
	if runtime.GOOS != "linux" || pid <= 0 || ctx.Err() != nil {
		return ErrDockerTopologyDrift
	}
	root, err := os.OpenRoot("/proc/" + strconv.Itoa(pid))
	if err != nil {
		return ErrDockerTopologyDrift
	}
	defer root.Close()
	return checkGatewayProcListeners(ctx, root, bridgeAddress)
}

func checkGatewayProcListeners(ctx context.Context, root *os.Root, bridgeAddress string) error {
	before, err := gatewayProcessStart(root)
	if err != nil {
		return ErrDockerTopologyDrift
	}
	owned, err := gatewaySocketDescriptors(root)
	if err != nil {
		return ErrDockerTopologyDrift
	}
	listeners := make(map[string]string)
	for _, name := range []string{"net/tcp", "net/tcp6"} {
		content, err := readGatewayProcFile(root, name, maximumGatewaySocketTable)
		if err != nil || collectGatewayListeners(content, name == "net/tcp6", owned, listeners, bridgeAddress) != nil {
			return ErrDockerTopologyDrift
		}
	}
	if listeners["1F90"] == "" || listeners["1F91"] == "" {
		return ErrDockerTopologyDrift
	}
	stillOwned, err := gatewaySocketDescriptors(root)
	if err != nil {
		return ErrDockerTopologyDrift
	}
	for _, inode := range listeners {
		if !stillOwned[inode] {
			return ErrDockerTopologyDrift
		}
	}
	after, err := gatewayProcessStart(root)
	if err != nil || before != after || ctx.Err() != nil {
		return ErrDockerTopologyDrift
	}
	return nil
}

func readGatewayProcFile(root *os.Root, name string, maximum int64) ([]byte, error) {
	file, err := root.Open(name)
	if err != nil {
		return nil, ErrDockerTopologyDrift
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(content)) > maximum || len(content) == 0 {
		return nil, ErrDockerTopologyDrift
	}
	return content, nil
}

func gatewayProcessStart(root *os.Root) (string, error) {
	content, err := readGatewayProcFile(root, "stat", 4096)
	if err != nil {
		return "", ErrDockerTopologyDrift
	}
	// The command name can contain spaces and parentheses. Field 22 is the
	// start tick, counted from the state field after the final parenthesis.
	end := strings.LastIndexByte(string(content), ')')
	if end < 0 {
		return "", ErrDockerTopologyDrift
	}
	fields := strings.Fields(string(content[end+1:]))
	if len(fields) < 20 || fields[0] == "Z" || fields[0] == "X" {
		return "", ErrDockerTopologyDrift
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || start == 0 {
		return "", ErrDockerTopologyDrift
	}
	return fields[19], nil
}

func gatewaySocketDescriptors(root *os.Root) (map[string]bool, error) {
	directory, err := root.Open("fd")
	if err != nil {
		return nil, ErrDockerTopologyDrift
	}
	defer directory.Close()
	names, err := directory.Readdirnames(4097)
	if (err != nil && err != io.EOF) || len(names) > 4096 {
		return nil, ErrDockerTopologyDrift
	}
	owned := make(map[string]bool)
	for _, name := range names {
		if _, err := strconv.ParseUint(name, 10, 32); err != nil {
			return nil, ErrDockerTopologyDrift
		}
		target, err := root.Readlink("fd/" + name)
		if os.IsNotExist(err) {
			continue // An unrelated transient descriptor may close during observation.
		}
		if err != nil {
			return nil, ErrDockerTopologyDrift
		}
		if strings.HasPrefix(target, "socket:[") && strings.HasSuffix(target, "]") {
			inode := strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")
			if number, err := strconv.ParseUint(inode, 10, 64); err != nil || number == 0 {
				return nil, ErrDockerTopologyDrift
			}
			owned[inode] = true
		}
	}
	return owned, nil
}

func collectGatewayListeners(content []byte, ipv6 bool, owned map[string]bool, listeners map[string]string, bridgeAddress string) error {
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) == 0 || !strings.HasPrefix(strings.TrimSpace(lines[0]), "sl") || !strings.Contains(lines[0], "local_address") || !strings.Contains(lines[0], "inode") {
		return ErrDockerTopologyDrift
	}
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			return ErrDockerTopologyDrift
		}
		if fields[3] != "0A" {
			continue
		}
		address, port, ok := strings.Cut(fields[1], ":")
		if !ok {
			return ErrDockerTopologyDrift
		}
		if port != "1F90" && port != "1F91" {
			continue // Only the two checked gateway ports are constrained here.
		}
		bytes, err := hex.DecodeString(address)
		key := port
		if port == "1F90" && address == bridgeAddress {
			key = "driver"
		}
		if err != nil || ipv6 || len(bytes) != 4 || (binary.NativeEndian.Uint32(bytes) != 0x7f000001 && key != "driver") || !owned[fields[9]] || listeners[key] != "" {
			return ErrDockerTopologyDrift
		}
		listeners[key] = fields[9]
	}
	return nil
}

// The pinned gateway configuration uses OpenShell v0.0.86's default sandbox
// network, openshell-docker. Docker's built-in bridge is a different network.
const gatewayBridgeInspection = `{"name":{{json .Name}},"driver":{{json .Driver}},"scope":{{json .Scope}},"ipamDriver":{{json .IPAM.Driver}},"config":[{{range $i,$c := .IPAM.Config}}{{if $i}},{{end}}{"subnet":{{json $c.Subnet}},"gateway":{{json $c.Gateway}}}{{end}}]}`

func (state *dockerTopologyState) gatewayBridgeAddress(ctx context.Context) (string, error) {
	output, err := state.runner.Run(ctx, state.environment, state.binary, "network", "inspect", "--format", gatewayBridgeInspection, "openshell-docker")
	if err != nil {
		return "", ErrDockerTopologyDrift
	}
	defer clear(output)
	return decodeGatewayBridge(output)
}

func decodeGatewayBridge(output []byte) (string, error) {
	var network struct {
		Name       string `json:"name"`
		Driver     string `json:"driver"`
		Scope      string `json:"scope"`
		IPAMDriver string `json:"ipamDriver"`
		Config     []struct {
			Subnet  string `json:"subnet"`
			Gateway string `json:"gateway"`
		} `json:"config"`
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	if len(output) == 0 || len(output) > runtimeTopologyMaxOutputBytes || decoder.Decode(&network) != nil || decoder.Decode(new(any)) != io.EOF || network.Name != "openshell-docker" || network.Driver != "bridge" || network.Scope != "local" || network.IPAMDriver != "default" || len(network.Config) != 1 {
		return "", ErrDockerTopologyDrift
	}
	address, err := netip.ParseAddr(network.Config[0].Gateway)
	subnet, subnetErr := netip.ParsePrefix(network.Config[0].Subnet)
	if err != nil || subnetErr != nil || !address.Is4() || !address.IsPrivate() || !subnet.Addr().Is4() || !subnet.Contains(address) {
		return "", ErrDockerTopologyDrift
	}
	value := address.As4()
	var encoded [4]byte
	binary.NativeEndian.PutUint32(encoded[:], binary.BigEndian.Uint32(value[:]))
	return strings.ToUpper(hex.EncodeToString(encoded[:])), nil
}
