//go:build darwin

package debugreport

import (
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	"golang.org/x/sys/unix"
)

func collectLocalHostDiagnostics(ctxCfg *config.Context) HostDiagnostics {
	diagnostics := HostDiagnostics{
		CPUCount:           runtime.NumCPU(),
		MemoryBytes:        -1,
		SwapBytes:          -1,
		DiskAvailableBytes: -1,
	}

	if memoryBytes, err := unix.SysctlUint64("hw.memsize"); err != nil {
		diagnostics.Issues = append(diagnostics.Issues, fmt.Sprintf("memory: %v", err))
	} else {
		diagnostics.MemoryBytes = int64(memoryBytes)
	}

	if swapBytes, err := readDarwinSwapTotal(); err != nil {
		diagnostics.Issues = append(diagnostics.Issues, fmt.Sprintf("swap: %v", err))
	} else {
		diagnostics.SwapBytes = swapBytes
	}

	if osVersion, err := readDarwinOSVersion(); err != nil {
		diagnostics.Issues = append(diagnostics.Issues, fmt.Sprintf("os: %v", err))
	} else {
		diagnostics.OSVersion = osVersion
	}

	availableDiskBytes, err := availableDiskBytes(ctxCfg)
	if err != nil {
		diagnostics.Issues = append(diagnostics.Issues, fmt.Sprintf("disk: %v", err))
	} else {
		diagnostics.DiskAvailableBytes = availableDiskBytes
	}

	return diagnostics
}

func readDarwinSwapTotal() (int64, error) {
	data, err := unix.SysctlRaw("vm.swapusage")
	if err != nil {
		return 0, err
	}
	if len(data) < 8 {
		return 0, fmt.Errorf("unexpected vm.swapusage length: %d", len(data))
	}
	return int64(binary.LittleEndian.Uint64(data[:8])), nil
}

func readDarwinOSVersion() (string, error) {
	data, err := os.ReadFile("/System/Library/CoreServices/SystemVersion.plist")
	if err != nil {
		return "", err
	}
	var plist struct {
		Dict struct {
			Nodes []struct {
				XMLName xml.Name
				Value   string `xml:",chardata"`
			} `xml:",any"`
		} `xml:"dict"`
	}
	if err := xml.Unmarshal(data, &plist); err != nil {
		return "", err
	}
	values := map[string]string{}
	var currentKey string
	for _, node := range plist.Dict.Nodes {
		value := strings.TrimSpace(node.Value)
		switch node.XMLName.Local {
		case "key":
			currentKey = value
		case "string":
			if currentKey != "" {
				values[currentKey] = value
				currentKey = ""
			}
		}
	}
	name := strings.TrimSpace(values["ProductName"])
	version := strings.TrimSpace(values["ProductVersion"])
	if name == "" || version == "" {
		return "", fmt.Errorf("missing ProductName or ProductVersion in SystemVersion.plist")
	}
	return fmt.Sprintf("%s %s", name, version), nil
}
