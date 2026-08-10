//go:build darwin || linux

package materialize

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

func copySource(ctx context.Context, source string, target string) error {
	fileDescriptor, err := openAbsoluteDirectoryNoFollow(source)
	if err != nil {
		return fmt.Errorf("open source directory without following links: %w", err)
	}
	return copyOpenDirectory(ctx, fileDescriptor, source, target)
}

func openAbsoluteDirectoryNoFollow(path string) (int, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return -1, fmt.Errorf("source path %q is not absolute", path)
	}
	fileDescriptor, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY, 0)
	if err != nil {
		return -1, err
	}
	if clean == string(filepath.Separator) {
		return fileDescriptor, nil
	}
	for _, segment := range strings.Split(strings.TrimPrefix(clean, string(filepath.Separator)), string(filepath.Separator)) {
		childDescriptor, openErr := unix.Openat(fileDescriptor, segment, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		unix.Close(fileDescriptor)
		if openErr != nil {
			return -1, openErr
		}
		fileDescriptor = childDescriptor
	}
	return fileDescriptor, nil
}

func copyOpenDirectory(ctx context.Context, fileDescriptor int, sourceLabel string, target string) error {
	directory := os.NewFile(uintptr(fileDescriptor), sourceLabel)
	if directory == nil {
		unix.Close(fileDescriptor)
		return fmt.Errorf("open source directory %q", sourceLabel)
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return fmt.Errorf("read source directory %q: %w", sourceLabel, err)
	}
	sort.Slice(entries, func(left int, right int) bool {
		return entries[left].Name() < entries[right].Name()
	})
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		pathLabel := filepath.Join(sourceLabel, name)
		before := unix.Stat_t{}
		if err := unix.Fstatat(fileDescriptor, name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return fmt.Errorf("inspect source path %q without following links: %w", pathLabel, err)
		}
		beforeType := before.Mode & unix.S_IFMT
		if beforeType == unix.S_IFLNK {
			return fmt.Errorf("source path %q must not be a symbolic link", filepath.Join(sourceLabel, name))
		}
		if beforeType != unix.S_IFDIR && beforeType != unix.S_IFREG {
			return fmt.Errorf("source path %q must be a real directory or regular file", pathLabel)
		}
		openFlags := unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK
		if beforeType == unix.S_IFDIR {
			openFlags |= unix.O_DIRECTORY
		}
		childDescriptor, err := unix.Openat(fileDescriptor, name, openFlags, 0)
		if err != nil {
			return fmt.Errorf("open source path %q without following links: %w", pathLabel, err)
		}
		stat := unix.Stat_t{}
		if err := unix.Fstat(childDescriptor, &stat); err != nil {
			unix.Close(childDescriptor)
			return fmt.Errorf("inspect opened source path %q: %w", pathLabel, err)
		}
		if stat.Dev != before.Dev || stat.Ino != before.Ino || stat.Mode&unix.S_IFMT != beforeType {
			unix.Close(childDescriptor)
			return fmt.Errorf("source path %q changed while it was being copied", pathLabel)
		}
		destination := filepath.Join(target, name)
		switch stat.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			if err := os.Mkdir(destination, 0700); err != nil {
				unix.Close(childDescriptor)
				return fmt.Errorf("create copied directory %q: %w", destination, err)
			}
			if err := os.Chmod(destination, 0700); err != nil {
				unix.Close(childDescriptor)
				return fmt.Errorf("protect copied directory %q: %w", destination, err)
			}
			if err := copyOpenDirectory(ctx, childDescriptor, pathLabel, destination); err != nil {
				return err
			}
		case unix.S_IFREG:
			if err := copyOpenRegularFile(childDescriptor, pathLabel, destination); err != nil {
				return err
			}
		default:
			unix.Close(childDescriptor)
			return fmt.Errorf("source path %q must be a real directory or regular file", pathLabel)
		}
	}
	return nil
}

func copyOpenRegularFile(fileDescriptor int, sourceLabel string, target string) error {
	input := os.NewFile(uintptr(fileDescriptor), sourceLabel)
	if input == nil {
		unix.Close(fileDescriptor)
		return fmt.Errorf("open source file %q", sourceLabel)
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("create copied file %q: %w", target, err)
	}
	if err := output.Chmod(0600); err != nil {
		output.Close()
		return fmt.Errorf("protect copied file %q: %w", target, err)
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return fmt.Errorf("copy source file %q: %w", sourceLabel, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close copied file %q: %w", target, closeErr)
	}
	return nil
}
