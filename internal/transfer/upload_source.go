package transfer

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"hash"
	"io"
	"os"
	"path/filepath"

	"github.com/Cdners/S3S/internal/core"
	"github.com/Cdners/S3S/internal/store"
)

const (
	maxUploadSize  int64 = 5 << 40
	memoryPartSize int64 = 8 << 20
)

// Even 5 TiB fits into 10,000 parts. Round larger parts to whole MiB.
func uploadPartSize(size int64) int64 {
	const mib int64 = 1 << 20
	needed := (size + 9999) / 10000
	return max(memoryPartSize, ((needed+mib-1)/mib)*mib)
}

type cancelReader struct {
	ctx context.Context
	r   io.Reader
}

func (r cancelReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

type localSource struct {
	f    *os.File
	info os.FileInfo
	id   string
}

func openSource(path string) (*localSource, error) {
	if !filepath.IsAbs(path) {
		return nil, core.NewError(uploadSourceError)
	}
	named, err := os.Lstat(path)
	if err != nil || !named.Mode().IsRegular() {
		return nil, core.NewError(uploadSourceError)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, core.NewError(uploadSourceError)
	}
	info, err := f.Stat()
	if err != nil || !os.SameFile(info, named) {
		f.Close()
		return nil, core.NewError(uploadSourceError)
	}
	id, _, err := fileIdentity(f)
	if err != nil {
		f.Close()
		return nil, core.NewError(uploadSourceError)
	}
	if info.Size() > maxUploadSize {
		f.Close()
		return nil, core.NewError("error.uploadSize")
	}
	return &localSource{f: f, info: info, id: id}, nil
}
func (s *localSource) unchanged() bool {
	named, err := os.Lstat(s.f.Name())
	return err == nil && os.SameFile(named, s.info) && named.Size() == s.info.Size() && named.ModTime().Equal(s.info.ModTime())
}
func (s *localSource) digest(ctx context.Context) (string, error) {
	h := sha256.New()
	n, err := io.Copy(h, cancelReader{ctx, io.NewSectionReader(s.f, 0, s.info.Size())})
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil || n != s.info.Size() || !s.unchanged() {
		return "", core.NewError(uploadSourceError)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (s *localSource) verify(ctx context.Context, t core.Task, u store.Upload) ([]string, error) {
	if s.id != u.FileID || s.info.Size() != t.Size || s.info.ModTime().UnixNano() != u.ModTimeNS || u.PartSize <= 0 {
		return nil, core.NewError(uploadSourceError)
	}
	// These part hashes describe the same input stream as the original
	// manifest's full-file SHA. A transiently modified range is never sent.
	whole := sha256.New()
	var digests []string
	for offset := int64(0); offset < t.Size; {
		length := min(u.PartSize, t.Size-offset)
		part := sha256.New()
		n, err := io.Copy(io.MultiWriter(whole, part), cancelReader{ctx, io.NewSectionReader(s.f, offset, length)})
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil || n != length {
			return nil, core.NewError(uploadSourceError)
		}
		digests = append(digests, hex.EncodeToString(part.Sum(nil)))
		offset += length
	}
	if !s.unchanged() || hex.EncodeToString(whole.Sum(nil)) != u.SHA256 {
		return nil, core.NewError(uploadSourceError)
	}
	return digests, nil
}

// A part snapshot keeps SDK seeks/retries independent of subsequent writes to
// the original file. Common parts use RAM; larger ones use anonymous scratch.
type partSnapshot struct {
	body     io.ReadSeeker
	scratch  *os.File
	sha, md5 string
}

func (p *partSnapshot) close() {
	if p.scratch != nil {
		p.scratch.Close()
	}
}
func (s *localSource) snapshot(ctx context.Context, offset, length int64, whole hash.Hash, needBody bool) (partSnapshot, error) {
	var p partSnapshot
	var buf bytes.Buffer
	sha := sha256.New()
	md := md5.New() // HTTP Content-MD5 for UploadPart; SHA-256 covers recovery.
	writers := []io.Writer{whole, sha, md}
	if needBody {
		if length <= memoryPartSize {
			buf.Grow(int(length))
			writers = append(writers, &buf)
		} else {
			f, err := newUploadScratch()
			if err != nil {
				return p, core.NewError(uploadSourceError)
			}
			p.scratch = f
			p.body = f
			writers = append(writers, f)
		}
	}
	n, err := io.Copy(io.MultiWriter(writers...), cancelReader{ctx, io.NewSectionReader(s.f, offset, length)})
	if err != nil || n != length || !s.unchanged() {
		p.close()
		if ctx.Err() != nil {
			return partSnapshot{}, ctx.Err()
		}
		return partSnapshot{}, core.NewError(uploadSourceError)
	}
	if needBody && p.scratch == nil {
		p.body = bytes.NewReader(buf.Bytes())
	}
	if p.scratch != nil {
		if _, err = p.scratch.Seek(0, io.SeekStart); err != nil {
			p.close()
			return partSnapshot{}, core.NewError(uploadSourceError)
		}
	}
	p.sha = hex.EncodeToString(sha.Sum(nil))
	p.md5 = base64.StdEncoding.EncodeToString(md.Sum(nil))
	return p, nil
}
