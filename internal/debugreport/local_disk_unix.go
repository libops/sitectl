//go:build linux || darwin

package debugreport

import "syscall"

func localAvailableDiskBytes(path string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	if stat.Bsize <= 0 {
		return 0, nil
	}
	return availableBytes(stat.Bavail, uint64(stat.Bsize)) // #nosec G115 -- block size is checked positive and availableBytes guards the product.
}
