package debugreport

import (
	"fmt"
	"math"
)

func availableBytes(blocks, blockSize uint64) (int64, error) {
	if blockSize == 0 {
		return 0, nil
	}
	if blocks > math.MaxInt64/blockSize {
		return 0, fmt.Errorf("available disk byte count overflows int64")
	}
	return int64(blocks * blockSize), nil // #nosec G115 -- guarded above to fit in int64.
}
