package core

const MaxBytesPerSecond int64 = 1 << 40

// Effective returns the canonical budgets for a selected mode. A zero budget
// is unlimited; custom budgets use integral object payload bytes per second.
func (o TransferOptions) Effective() (TransferOptions, error) {
	switch o.Mode {
	case FullSpeed:
		return TransferOptions{Mode: FullSpeed}, nil
	case Office:
		return TransferOptions{Mode: Office, UploadBytesPerSecond: 1 << 20, DownloadBytesPerSecond: 4 << 20}, nil
	case Custom:
		if o.UploadBytesPerSecond < 0 || o.UploadBytesPerSecond > MaxBytesPerSecond || o.DownloadBytesPerSecond < 0 || o.DownloadBytesPerSecond > MaxBytesPerSecond {
			return TransferOptions{}, NewError("error.transferOptions")
		}
		return o, nil
	default:
		return TransferOptions{}, NewError("error.transferOptions")
	}
}
