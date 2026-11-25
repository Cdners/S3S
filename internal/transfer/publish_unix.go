//go:build !windows

package transfer

import (
	"fmt"
	"os"
	"syscall"
)

func publish(from, to string) error { return os.Link(from, to) }

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func fileIdentity(f *os.File) (string, uint64, error) {
	info, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	stat := info.Sys().(*syscall.Stat_t)
	return fmt.Sprintf("%x:%x", stat.Dev, stat.Ino), uint64(stat.Nlink), nil
}

func writableLinks(_ string, _ os.FileInfo, links uint64) bool { return links == 1 }
