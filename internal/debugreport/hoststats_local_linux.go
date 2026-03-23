//go:build linux

package debugreport

import (
	"fmt"
	"os"
	"runtime"

	"github.com/libops/sitectl/pkg/config"
)

func collectLocalHostDiagnostics(ctxCfg *config.Context) HostDiagnostics {
	diagnostics := HostDiagnostics{
		CPUCount:           runtime.NumCPU(),
		MemoryBytes:        -1,
		SwapBytes:          -1,
		DiskAvailableBytes: -1,
	}

	meminfo, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		diagnostics.Issues = append(diagnostics.Issues, fmt.Sprintf("memory: %v", err))
	} else {
		memoryBytes, swapBytes, parseErr := ParseMemInfo(string(meminfo))
		if parseErr != nil {
			diagnostics.Issues = append(diagnostics.Issues, fmt.Sprintf("memory: %v", parseErr))
		} else {
			diagnostics.MemoryBytes = memoryBytes
			diagnostics.SwapBytes = swapBytes
		}
	}

	osRelease, err := os.ReadFile("/etc/os-release")
	if err != nil {
		diagnostics.Issues = append(diagnostics.Issues, fmt.Sprintf("os: %v", err))
	} else if osVersion := parseOSRelease(string(osRelease)); osVersion != "" {
		diagnostics.OSVersion = osVersion
	} else {
		diagnostics.Issues = append(diagnostics.Issues, "os: PRETTY_NAME not found in /etc/os-release")
	}

	availableDiskBytes, err := availableDiskBytes(ctxCfg)
	if err != nil {
		diagnostics.Issues = append(diagnostics.Issues, fmt.Sprintf("disk: %v", err))
	} else {
		diagnostics.DiskAvailableBytes = availableDiskBytes
	}

	return diagnostics
}
