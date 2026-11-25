package transfer

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// Retain the partial's inode across publication, including a crash after the
// rename but before SQLite records completion. MoveFileEx without REPLACE_EXISTING
// refuses any existing destination. WRITE_THROUGH requests durable publication.
func publish(from, to string) error {
	link := from + ".publish"
	if err := os.Link(from, link); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		p, err := os.Lstat(from)
		l, err2 := os.Lstat(link)
		if err != nil || err2 != nil || !os.SameFile(p, l) {
			return os.ErrExist
		}
	}
	src, err := windows.UTF16PtrFromString(link)
	if err != nil {
		return err
	}
	dst, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(src, dst, windows.MOVEFILE_WRITE_THROUGH)
}

// Windows has no portable fsync for directories. Files use FlushFileBuffers
// through os.File.Sync, and final publication uses MOVEFILE_WRITE_THROUGH.
func syncDirectory(string) error { return nil }

func fileIdentity(f *os.File) (string, uint64, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		return "", 0, err
	}
	return fmt.Sprintf("%x:%x:%x", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow), uint64(info.NumberOfLinks), nil
}

func writableLinks(partial string, info os.FileInfo, links uint64) bool {
	if links == 1 {
		return true
	}
	if links != 2 {
		return false
	}
	// The only permitted second private name belongs to an interrupted MoveFileEx.
	alias, err := os.Lstat(partial + ".publish")
	return err == nil && os.SameFile(info, alias)
}
