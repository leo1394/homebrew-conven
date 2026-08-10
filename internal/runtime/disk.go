package runtime

import (
	"fmt"
	"syscall"
)

const (
	minimumBuildDiskSpace = uint64(512 * 1024 * 1024)
	warningBuildDiskSpace = uint64(2 * 1024 * 1024 * 1024)
)

func checkBuildDiskSpace(path string) (uint64, error) {
	available, err := availableDiskSpace(path)
	if err != nil {
		return 0, fmt.Errorf("check disk space for service builds at %q: %w", path, err)
	}
	if err := validateBuildDiskSpace(path, available); err != nil {
		return available, err
	}
	return available, nil
}

func availableDiskSpace(path string) (uint64, error) {
	var status syscall.Statfs_t
	if err := syscall.Statfs(path, &status); err != nil {
		return 0, err
	}
	if status.Bsize <= 0 {
		return 0, fmt.Errorf("filesystem reported invalid block size %d", status.Bsize)
	}
	blockSize := uint64(status.Bsize)
	availableBlocks := uint64(status.Bavail)
	if availableBlocks > ^uint64(0)/blockSize {
		return 0, fmt.Errorf("filesystem reported an unsupported available size")
	}
	return availableBlocks * blockSize, nil
}

func validateBuildDiskSpace(path string, available uint64) error {
	if available >= minimumBuildDiskSpace {
		return nil
	}
	return fmt.Errorf("insufficient disk space for service builds at %q: %s available; at least %s is required. Free disk space before starting services", path, formatDiskSpace(available), formatDiskSpace(minimumBuildDiskSpace))
}

func buildDiskSpaceWarning(path string, available uint64) string {
	if available < minimumBuildDiskSpace || available >= warningBuildDiskSpace {
		return ""
	}
	return fmt.Sprintf("low disk space for service builds at %q: %s available; at least %s is recommended. Free disk space to avoid dependency download and build failures", path, formatDiskSpace(available), formatDiskSpace(warningBuildDiskSpace))
}

func formatDiskSpace(bytes uint64) string {
	const mebibyte = uint64(1024 * 1024)
	const gibibyte = uint64(1024 * 1024 * 1024)
	if bytes >= gibibyte {
		return fmt.Sprintf("%.2f GiB", float64(bytes)/float64(gibibyte))
	}
	return fmt.Sprintf("%.2f MiB", float64(bytes)/float64(mebibyte))
}
