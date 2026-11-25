//go:build !windows

package transfer

import "os"

func newUploadScratch() (*os.File, error) {
	f, err := os.CreateTemp("", "s3s-upload-*")
	if err != nil {
		return nil, err
	}
	if err := os.Remove(f.Name()); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
