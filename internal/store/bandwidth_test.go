package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/Cdners/S3S/internal/core"
)

func TestTransferOptionsMigrateAndPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	old, err := sql.Open("sqlite3", path)
	requireOK(t, err)
	_, err = old.Exec(schema1 + schema2 + schema3 + "PRAGMA user_version=3;")
	requireOK(t, err)
	requireOK(t, old.Close())
	s := openDB(t, path)
	ctx := t.Context()
	defaults, err := s.TransferOptions(ctx)
	requireOK(t, err)
	if defaults != (core.TransferOptions{Mode: core.FullSpeed}) {
		t.Fatalf("default: %+v", defaults)
	}
	office, err := s.SaveTransferOptions(ctx, core.TransferOptions{Mode: core.Office})
	requireOK(t, err)
	if office.UploadBytesPerSecond != 1<<20 || office.DownloadBytesPerSecond != 4<<20 {
		t.Fatalf("office: %+v", office)
	}
	custom := core.TransferOptions{Mode: core.Custom, DownloadBytesPerSecond: core.MaxBytesPerSecond}
	_, err = s.SaveTransferOptions(ctx, custom)
	requireOK(t, err)
	for _, invalid := range []core.TransferOptions{{Mode: "unknown"}, {Mode: core.Custom, UploadBytesPerSecond: -1}, {Mode: core.Custom, DownloadBytesPerSecond: core.MaxBytesPerSecond + 1}} {
		_, err = s.SaveTransferOptions(ctx, invalid)
		requireCode(t, err, "error.transferOptions")
	}
	requireOK(t, s.Close())
	s = openDB(t, path)
	saved, err := s.TransferOptions(ctx)
	requireOK(t, err)
	if saved != custom {
		t.Fatalf("lost options: %+v", saved)
	}
}
