//go:build !linux && !darwin

package debugreport

import (
	"fmt"
	"runtime"

	"github.com/libops/sitectl/pkg/config"
)

func collectLocalHostDiagnostics(ctxCfg *config.Context) HostDiagnostics {
	diagnostics := HostDiagnostics{
		CPUCount:           runtime.NumCPU(),
		MemoryBytes:        -1,
		SwapBytes:          -1,
		DiskAvailableBytes: -1,
		Issues:             []string{"local host diagnostics are not implemented for this platform"},
	}
	availableDiskBytes, err := availableDiskBytes(ctxCfg)
	if err != nil {
		diagnostics.Issues = append(diagnostics.Issues, fmt.Sprintf("disk: %v", err))
	} else {
		diagnostics.DiskAvailableBytes = availableDiskBytes
	}
	return diagnostics
}
