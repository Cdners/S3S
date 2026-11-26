package store

import (
	"context"

	"github.com/Cdners/S3S/internal/core"
)

func (s *Store) TransferOptions(ctx context.Context) (core.TransferOptions, error) {
	var o core.TransferOptions
	err := s.db.QueryRowContext(ctx, "SELECT mode,upload_rate,download_rate FROM transfer_options WHERE singleton=1").
		Scan(&o.Mode, &o.UploadBytesPerSecond, &o.DownloadBytesPerSecond)
	if err != nil {
		return core.TransferOptions{}, dbError(err)
	}
	return o.Effective()
}

func (s *Store) SaveTransferOptions(ctx context.Context, options core.TransferOptions) (core.TransferOptions, error) {
	o, err := options.Effective()
	if err != nil {
		return core.TransferOptions{}, err
	}
	err = requireMatch(s.db.ExecContext(ctx, "UPDATE transfer_options SET mode=?,upload_rate=?,download_rate=? WHERE singleton=1", o.Mode, o.UploadBytesPerSecond, o.DownloadBytesPerSecond))
	return o, err
}
