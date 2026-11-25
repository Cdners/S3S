package transfer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"slices"
	"time"

	"github.com/Cdners/S3S/internal/core"
	"github.com/Cdners/S3S/internal/store"
)

const (
	uploadSourceError core.ErrorCode = "error.uploadSource"
	unknownCreate     core.ErrorCode = "error.uploadCreateUnknown"
	verifyError       core.ErrorCode = "error.uploadVerify"
	partsError        core.ErrorCode = "error.uploadParts"
	missingSession    core.ErrorCode = "error.uploadSessionMissing"
	objectExists      core.ErrorCode = "error.uploadExists"
)

type Destination interface {
	BeginUpload(ctx context.Context, bucket, key, nonce, sha256Hex string) (string, error)
	UploadPart(ctx context.Context, bucket, key, uploadID string, number int, body io.ReadSeeker, size int64, md5Base64 string) (string, error)
	ListParts(ctx context.Context, bucket, key, uploadID string) ([]core.Part, error)
	CompleteUpload(ctx context.Context, bucket, key, uploadID string, parts []core.Part) error
	// AbortUpload returns nil only after confirming the bound multipart session is gone.
	AbortUpload(ctx context.Context, bucket, key, uploadID string) error
	PutEmpty(ctx context.Context, bucket, key, nonce, sha256Hex string) error
	VerifyUpload(ctx context.Context, bucket, key, nonce, sha256Hex string, size int64) (bool, error)
}

func (e *Engine) EnqueueUpload(ctx context.Context, c core.Connection, bucket, key, path string) (core.Task, error) {
	if bucket == "" || key == "" {
		return core.Task{}, core.NewError(core.InvalidTask)
	}
	src, err := openSource(path)
	if err != nil {
		return core.Task{}, err
	}
	defer src.f.Close()
	if c.Provider == core.R2 && src.info.Size() > maxUploadSize-(5<<30) {
		return core.Task{}, core.NewError("error.uploadSize")
	}
	digest, err := src.digest(ctx)
	if err != nil {
		return core.Task{}, err
	}
	t := core.Task{ID: rand.Text(), ConnectionID: c.ID, Direction: core.Upload, Bucket: bucket, ObjectKey: key, LocalPath: path,
		Size: src.info.Size(), State: core.Queued, Resumable: true, UploadPhase: "ready"}
	u := store.Upload{Provider: c.Provider, Region: c.Region, Endpoint: c.Endpoint, FileID: src.id, SHA256: digest,
		ModTimeNS: src.info.ModTime().UnixNano(), PartSize: uploadPartSize(t.Size), Phase: "ready"}
	if err = e.db.CreateUpload(ctx, t, u); err != nil {
		return core.Task{}, err
	}
	return t, nil
}

func (e *Engine) RunUpload(ctx context.Context, id string, c core.Connection, dest Destination) error {
	if err := e.db.ClaimUpload(ctx, id); err != nil {
		return err
	}
	return e.RunUploadClaimed(ctx, id, c, dest)
}

type uploadWorker struct {
	db   *store.Store
	t    core.Task
	u    store.Upload
	dest Destination
}

func (e *Engine) RunUploadClaimed(ctx context.Context, id string, c core.Connection, dest Destination) error {
	t, err := e.db.Task(ctx, id)
	if err == nil && (t.State != core.Running || t.Direction != core.Upload || !t.Resumable) {
		return core.NewError(core.Conflict)
	}
	state := core.Completed
	if err == nil {
		var u store.Upload
		u, err = e.db.Upload(ctx, id)
		if err == nil {
			w := uploadWorker{e.db, t, u, dest}
			state, err = w.run(ctx, c)
		}
	}
	var detail *core.Error
	if err != nil {
		state = core.Failed
		if ctx.Err() != nil {
			state = core.Paused
			err = ctx.Err()
		} else if !errors.As(err, &detail) {
			detail = core.NewError(verifyError)
			err = detail
		}
	}
	if persist := e.db.FinishUpload(context.WithoutCancel(ctx), id, state, detail); persist != nil {
		return persist
	}
	return err
}

func (w *uploadWorker) run(ctx context.Context, c core.Connection) (core.TaskState, error) {
	localCancel := w.u.CancelRequested && w.t.UploadID == "" && (w.u.Phase == "ready" || w.u.Phase == "aborting")
	if !localCancel && (c.ID != w.t.ConnectionID || c.Provider != w.u.Provider || c.Endpoint != w.u.Endpoint || c.Region != w.u.Region || w.dest == nil) {
		return "", core.NewError("error.uploadIdentity")
	}
	if w.u.CancelRequested {
		return w.cancel(ctx)
	}
	if w.u.Phase == "creating" {
		return "", core.NewError(unknownCreate)
	}
	if w.u.Phase == "completing" {
		return core.Completed, w.resumeCommit(ctx)
	}
	if w.u.Phase != "ready" && w.u.Phase != "active" {
		return "", core.NewError(core.InvalidTransition)
	}
	src, err := openSource(w.t.LocalPath)
	if err != nil {
		return "", err
	}
	defer src.f.Close()
	digests, err := src.verify(ctx, w.t, w.u)
	if err != nil {
		return "", err
	}
	return core.Completed, w.send(ctx, src, digests)
}

func (w *uploadWorker) mark(ctx context.Context, next string) error {
	if err := w.db.MarkUploadPhase(ctx, w.t.ID, w.u.Phase, next); err != nil {
		return err
	}
	w.u.Phase = next
	return nil
}
func hasCode(err error, code core.ErrorCode) bool {
	var e *core.Error
	return errors.As(err, &e) && e.Code == code
}
func definiteRejection(err error) bool {
	return hasCode(err, "error.cloudAccess") || hasCode(err, "error.cloudMissing") || hasCode(err, "error.cloudCredentials") || hasCode(err, core.InvalidTask)
}
func (w *uploadWorker) verify(ctx context.Context) (bool, error) {
	return w.dest.VerifyUpload(ctx, w.t.Bucket, w.t.ObjectKey, w.t.ID, w.u.SHA256, w.t.Size)
}
func (w *uploadWorker) begin(ctx context.Context) error {
	if err := w.mark(ctx, "creating"); err != nil {
		return err
	}
	id, err := w.dest.BeginUpload(ctx, w.t.Bucket, w.t.ObjectKey, w.t.ID, w.u.SHA256)
	// Persist an acknowledged ID even if the caller paused during Create.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if err != nil || id == "" {
		if definiteRejection(err) {
			if reset := w.mark(cleanup, "ready"); reset != nil {
				return reset
			}
			return err
		}
		// An unacknowledged Create cannot be reconciled by listing sessions
		// for the same key: those may belong to another client.
		out := core.NewError(unknownCreate)
		var e *core.Error
		if errors.As(err, &e) {
			out.RequestID = e.RequestID
			out.ProviderCode = e.ProviderCode
		}
		return out
	}
	if err = w.db.BindUpload(cleanup, w.t.ID, id); err != nil {
		// Never leave a known unbound ID running just because the SQL write failed.
		if abort := w.dest.AbortUpload(cleanup, w.t.Bucket, w.t.ObjectKey, id); abort == nil {
			_ = w.mark(cleanup, "ready")
		}
		return err
	}
	w.t.UploadID = id
	w.u.Phase = "active"
	return nil
}

func (w *uploadWorker) send(ctx context.Context, src *localSource, digests []string) error {
	// A cheap existence check avoids filling a doomed multipart session.
	// The destination still applies If-None-Match to the actual publication.
	own, err := w.verify(ctx)
	if err != nil {
		return err
	}
	if own {
		return core.NewError(objectExists)
	}
	if w.t.Size == 0 {
		if err = w.mark(ctx, "completing"); err != nil {
			return err
		}
		return w.putEmpty(ctx, true)
	}
	if w.u.Phase == "ready" {
		if err = w.begin(ctx); err != nil {
			return err
		}
	}
	remote, err := w.dest.ListParts(ctx, w.t.Bucket, w.t.ObjectKey, w.t.UploadID)
	if hasCode(err, missingSession) {
		if err = w.db.RestartUpload(ctx, w.t.ID, w.t.UploadID); err != nil {
			return err
		}
		w.u.Phase = "ready"
		w.t.UploadID = ""
		if err = w.begin(ctx); err != nil {
			return err
		}
		remote, err = w.dest.ListParts(ctx, w.t.Bucket, w.t.ObjectKey, w.t.UploadID)
	}
	if err != nil {
		return err
	}
	ack, err := w.matchParts(ctx, remote)
	if err != nil {
		return err
	}
	ack = slices.DeleteFunc(ack, func(p core.Part) bool {
		return p.Number <= 0 || p.Number > len(digests) || digests[p.Number-1] != p.Checksum
	})
	if err = w.db.ReconcileUploadParts(ctx, w.t.ID, w.t.UploadID, ack); err != nil {
		return err
	}

	matches := make(map[int]core.Part, len(ack))
	for _, p := range ack {
		matches[p.Number] = p
	}
	whole := sha256.New()
	var parts []core.Part
	for n, offset := 1, int64(0); offset < w.t.Size; n++ {
		length := min(w.u.PartSize, w.t.Size-offset)
		p, skip := matches[n]
		snap, err := src.snapshot(ctx, offset, length, whole, !skip)
		if err != nil {
			return err
		}
		if snap.sha != digests[n-1] {
			snap.close()
			return core.NewError(uploadSourceError)
		}
		if !skip {
			p = core.Part{Number: n, Offset: offset, Size: length, Checksum: snap.sha}
			p.ETag, err = w.dest.UploadPart(ctx, w.t.Bucket, w.t.ObjectKey, w.t.UploadID, n, snap.body, length, snap.md5)
			snap.close()
			if err != nil {
				return err
			}
			if err = w.db.SaveUploadPart(ctx, w.t.ID, w.t.UploadID, p); err != nil {
				return err
			}
		}
		parts = append(parts, p)
		offset += length
	}
	if hex.EncodeToString(whole.Sum(nil)) != w.u.SHA256 {
		return core.NewError(uploadSourceError)
	}
	if _, err = src.verify(ctx, w.t, w.u); err != nil {
		return err
	}
	if err = w.mark(ctx, "completing"); err != nil {
		return err
	}
	return w.commit(ctx, parts)
}

// Match only previously acknowledged local parts. Unknown remote parts are
// replaced from the source, irrespective of what an opaque ETag looks like.
func (w *uploadWorker) matchParts(ctx context.Context, remote []core.Part) ([]core.Part, error) {
	local, err := w.db.Parts(ctx, w.t.ID)
	if err != nil {
		return nil, err
	}
	byNumber := make(map[int]core.Part, len(remote))
	for _, p := range remote {
		if p.Number <= 0 || p.Number > 10000 || p.Size <= 0 || p.ETag == "" {
			return nil, core.NewError(partsError)
		}
		if _, dup := byNumber[p.Number]; dup {
			return nil, core.NewError(partsError)
		}
		byNumber[p.Number] = p
	}
	out := make([]core.Part, 0, len(local))
	for _, p := range local {
		r, ok := byNumber[p.Number]
		if ok && p.ETag == r.ETag && p.Size == r.Size {
			out = append(out, p)
		}
	}
	return out, nil
}

func (w *uploadWorker) resumeCommit(ctx context.Context) error {
	own, err := w.verify(ctx)
	if own || err != nil {
		return err
	}
	// The complete response may have been lost. Only repeat publication with
	// the identical parts that previously passed full source verification.
	src, err := openSource(w.t.LocalPath)
	if err != nil {
		return err
	}
	defer src.f.Close()
	if _, err = src.verify(ctx, w.t, w.u); err != nil {
		return err
	}
	if w.t.Size == 0 {
		return w.putEmpty(ctx, false)
	}
	remote, err := w.dest.ListParts(ctx, w.t.Bucket, w.t.ObjectKey, w.t.UploadID)
	if hasCode(err, missingSession) {
		return core.NewError(verifyError)
	}
	if err != nil {
		return err
	}
	parts, err := w.matchParts(ctx, remote)
	if err != nil {
		return err
	}
	want := (w.t.Size + w.u.PartSize - 1) / w.u.PartSize
	if int64(len(parts)) != want {
		return core.NewError(partsError)
	}
	return w.commit(ctx, parts)
}
func (w *uploadWorker) putEmpty(ctx context.Context, firstAttempt bool) error {
	err := w.dest.PutEmpty(ctx, w.t.Bucket, w.t.ObjectKey, w.t.ID, w.u.SHA256)
	if firstAttempt && (definiteRejection(err) || hasCode(err, objectExists) || hasCode(err, core.Conflict)) {
		// A rejected initial PUT did not publish. Preserve this fact even if
		// Cancel raced its response. A rejected retry cannot fence older PUTs.
		if reset := w.mark(context.WithoutCancel(ctx), "ready"); reset != nil {
			return reset
		}
		return err
	}
	return w.confirmPublication(ctx, err)
}
func (w *uploadWorker) commit(ctx context.Context, parts []core.Part) error {
	err := w.dest.CompleteUpload(ctx, w.t.Bucket, w.t.ObjectKey, w.t.UploadID, parts)
	return w.confirmPublication(ctx, err)
}
func (w *uploadWorker) confirmPublication(ctx context.Context, publishErr error) error {
	verifyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	own, err := w.verify(verifyCtx)
	if own && err == nil {
		return nil
	}
	if publishErr != nil {
		return publishErr
	}
	if err != nil {
		return err
	}
	return core.NewError(verifyError)
}

func (w *uploadWorker) cancel(ctx context.Context) (core.TaskState, error) {
	if w.u.Phase == "creating" && w.t.UploadID == "" {
		return "", core.NewError(unknownCreate)
	}
	if w.u.Phase == "completing" {
		own, err := w.verify(ctx)
		if own && err == nil {
			return core.Completed, nil
		}
		if err != nil && !hasCode(err, objectExists) {
			return "", err
		}
		// No multipart ID exists for an empty conditional PUT. With an
		// absent object we cannot fence a delayed request from another run.
		if w.t.UploadID == "" && err == nil {
			return "", core.NewError(verifyError)
		}
	}
	if w.u.Phase != "aborting" {
		if err := w.mark(ctx, "aborting"); err != nil {
			return "", err
		}
	}
	if w.t.UploadID == "" {
		return core.Canceled, nil
	}
	if err := w.dest.AbortUpload(ctx, w.t.Bucket, w.t.ObjectKey, w.t.UploadID); err != nil {
		return "", err
	}
	// AbortUpload confirms this session disappeared. A failed cleanup
	// leaves its ID and abort intent durable for retry.
	own, err := w.verify(ctx)
	if own && err == nil {
		return core.Completed, nil
	}
	if err != nil && !hasCode(err, objectExists) {
		return "", err
	}
	return core.Canceled, nil
}
