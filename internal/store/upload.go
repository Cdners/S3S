package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"

	"github.com/Cdners/S3S/internal/core"
)

// Upload binds recovery to a source file and destination. The task ID is also
// the immutable ownership marker written to object metadata by the adapter.
type Upload struct {
	Provider            core.Provider
	Region, Endpoint    string
	FileID, SHA256      string
	ModTimeNS, PartSize int64
	Phase               string
	CancelRequested     bool
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (s *Store) uploadTransaction(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return dbError(tx.Commit())
}

func (s *Store) CreateUpload(ctx context.Context, t core.Task, u Upload) error {
	if err := t.ValidateNew(); err != nil {
		return err
	}
	if t.Direction != core.Upload || t.LocalPath == "" || u.FileID == "" || !validDigest(u.SHA256) ||
		u.PartSize <= 0 || (t.Size > 0 && (t.Size-1)/u.PartSize >= 10000) ||
		(u.Phase != "" && u.Phase != "ready") || u.CancelRequested {
		return core.NewError(core.InvalidTask)
	}
	return s.uploadTransaction(ctx, func(tx *sql.Tx) error {
		// Enforce the selected connection scope even if the editor saved
		// different endpoint settings while the file was being hashed.
		if err := requireMatch(tx.ExecContext(ctx, `INSERT INTO tasks
            (id,connection_id,direction,bucket,object_key,local_path,size,state)
            SELECT ?,id,'upload',?,?,?,?,'queued' FROM connections
            WHERE id=? AND provider=? AND region=? AND endpoint=?`,
			t.ID, t.Bucket, t.ObjectKey, t.LocalPath, t.Size, t.ConnectionID, u.Provider, u.Region, u.Endpoint)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO uploads
            (task_id,provider,region,endpoint,file_id,mod_time_ns,source_sha256,part_size)
            VALUES (?,?,?,?,?,?,?,?)`, t.ID, u.Provider, u.Region, u.Endpoint, u.FileID, u.ModTimeNS, u.SHA256, u.PartSize)
		return dbError(err)
	})
}

func (s *Store) Upload(ctx context.Context, id string) (Upload, error) {
	var u Upload
	err := s.db.QueryRowContext(ctx, `SELECT provider,region,endpoint,file_id,mod_time_ns,source_sha256,part_size,phase,cancel_requested
        FROM uploads WHERE task_id=?`, id).Scan(&u.Provider, &u.Region, &u.Endpoint, &u.FileID, &u.ModTimeNS, &u.SHA256, &u.PartSize, &u.Phase, &u.CancelRequested)
	return u, dbError(err)
}

// RecoverUploads runs while the desktop process owns the workspace lock.
// Keep operation and cancellation intents intact for remote reconciliation.
func (s *Store) RecoverUploads(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET state='paused'
        WHERE state='running' AND id IN (SELECT task_id FROM uploads)`)
	return dbError(err)
}

func (s *Store) ClaimUpload(ctx context.Context, id string) error {
	return s.uploadTransaction(ctx, func(tx *sql.Tx) error {
		if err := requireMatch(tx.ExecContext(ctx, `UPDATE tasks SET state='running' WHERE id=?
            AND state IN ('paused','failed','queued') AND id IN (SELECT task_id FROM uploads)`, id)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE uploads SET last_error=NULL WHERE task_id=?", id)
		return dbError(err)
	})
}

// RequestUploadCancel persists the user's intent before stopping the worker.
// In particular, do not discard "completing": a lost response may mean the
// final object has already been committed by the provider.
func (s *Store) RequestUploadCancel(ctx context.Context, id string) error {
	return requireMatch(s.db.ExecContext(ctx, `UPDATE uploads SET cancel_requested=1 WHERE task_id=?
        AND task_id IN (SELECT id FROM tasks WHERE state IN ('running','queued','paused','failed'))`, id))
}

func (s *Store) MarkUploadPhase(ctx context.Context, id, from, to string) error {
	var guard string
	switch {
	case from == "creating" && to == "ready":
		// Only after a definitive rejection or successful cleanup of an unbound session.
		guard = "t.upload_id=''"
	case from == "completing" && to == "ready":
		// The first empty PUT was definitively rejected. No multipart session
		// or earlier ambiguous write may be discarded by this transition.
		guard = "t.size=0 AND t.upload_id=''"
	case from == "ready" && to == "creating":
		guard = "cancel_requested=0 AND t.size>0"
	case from == "ready" && to == "completing":
		guard = "cancel_requested=0 AND t.size=0"
	case from == "active" && to == "completing":
		// Every stored checkpoint has fixed geometry. The sum can equal the
		// file size only when every range is present.
		guard = "cancel_requested=0 AND t.upload_id<>'' AND t.size=(SELECT SUM(size) FROM parts WHERE task_id=t.id)"
	case (from == "ready" || from == "creating" || from == "active" || from == "completing") && to == "aborting":
		guard = "cancel_requested=1"
	default:
		return core.NewError(core.InvalidTransition)
	}
	return requireMatch(s.db.ExecContext(ctx, `UPDATE uploads SET phase=? WHERE task_id=? AND phase=? AND EXISTS
        (SELECT 1 FROM tasks t WHERE t.id=uploads.task_id AND t.state='running' AND `+guard+")", to, id, from))
}

// BindUpload must still capture an acknowledged session if the user requested
// cancellation while CreateMultipartUpload was in flight.
func (s *Store) BindUpload(ctx context.Context, id, uploadID string) error {
	if uploadID == "" {
		return core.NewError(core.InvalidTask)
	}
	return s.uploadTransaction(ctx, func(tx *sql.Tx) error {
		if err := requireMatch(tx.ExecContext(ctx, `UPDATE tasks SET upload_id=? WHERE id=? AND direction='upload'
            AND state='running' AND upload_id IN ('',?) AND NOT EXISTS
            (SELECT 1 FROM uploads WHERE task_id=tasks.id AND phase NOT IN ('creating','active'))`, uploadID, id, uploadID)); err != nil {
			return err
		}
		// Legacy tasks may have an upload ID but no v3 manifest.
		_, err := tx.ExecContext(ctx, "UPDATE uploads SET phase='active' WHERE task_id=?", id)
		return dbError(err)
	})
}

// RestartUpload is called only after confirming the bound session has ceased
// to exist. A completion intent must be resolved separately before restarting.
func (s *Store) RestartUpload(ctx context.Context, id, expectedID string) error {
	if expectedID == "" {
		return core.NewError(core.InvalidTask)
	}
	return s.uploadTransaction(ctx, func(tx *sql.Tx) error {
		if err := requireMatch(tx.ExecContext(ctx, `UPDATE uploads SET phase='ready' WHERE task_id=?
            AND phase='active' AND cancel_requested=0 AND task_id IN
            (SELECT id FROM tasks WHERE state='running' AND upload_id=?)`, id, expectedID)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE tasks SET upload_id='' WHERE id=?", id); err != nil {
			return dbError(err)
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM parts WHERE task_id=?", id)
		return dbError(err)
	})
}

// SaveUploadPart records a single acknowledged write. ReconcileUploadParts
// atomically replaces the local set with the caller's verified intersection of
// local and remote parts after ListParts has finished all its pages.
func (s *Store) SaveUploadPart(ctx context.Context, id, uploadID string, p core.Part) error {
	return s.writeUploadParts(ctx, id, uploadID, []core.Part{p}, false)
}

func (s *Store) ReconcileUploadParts(ctx context.Context, id, uploadID string, parts []core.Part) error {
	return s.writeUploadParts(ctx, id, uploadID, parts, true)
}

func (s *Store) writeUploadParts(ctx context.Context, id, uploadID string, parts []core.Part, replace bool) error {
	if uploadID == "" {
		return core.NewError(core.InvalidTask)
	}
	return s.uploadTransaction(ctx, func(tx *sql.Tx) error {
		var size, partSize int64
		var actualID, state, phase string
		if err := tx.QueryRowContext(ctx, `SELECT t.size,u.part_size,t.upload_id,t.state,u.phase FROM uploads u
            JOIN tasks t ON t.id=u.task_id WHERE t.id=?`, id).Scan(&size, &partSize, &actualID, &state, &phase); err != nil {
			return dbError(err)
		}
		if actualID != uploadID || state != "running" || phase != "active" {
			return core.NewError(core.Conflict)
		}
		if replace {
			if _, err := tx.ExecContext(ctx, "DELETE FROM parts WHERE task_id=?", id); err != nil {
				return dbError(err)
			}
		}
		insert := `INSERT INTO parts (task_id,number,offset,size,etag,checksum) VALUES(?,?,?,?,?,?)`
		if !replace {
			// Replayed acknowledgements can carry a new provider ETag for
			// identical bytes. A changed source checksum is a conflict.
			insert += ` ON CONFLICT(task_id,number) DO UPDATE SET etag=excluded.etag WHERE parts.checksum=excluded.checksum`
		}
		stmt, err := tx.PrepareContext(ctx, insert)
		if err != nil {
			return dbError(err)
		}
		defer stmt.Close()
		for _, p := range parts {
			if p.Offset < 0 || p.Offset >= size || p.Number < 1 || p.Number > 10000 || p.Offset%partSize != 0 ||
				int64(p.Number-1) != p.Offset/partSize || p.Size != min(partSize, size-p.Offset) || p.ETag == "" || !validDigest(p.Checksum) {
				return core.NewError(core.InvalidPart)
			}
			if err := requireMatch(stmt.ExecContext(ctx, id, p.Number, p.Offset, p.Size, p.ETag, p.Checksum)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) FinishUpload(ctx context.Context, id string, state core.TaskState, detail *core.Error) error {
	var guard string
	switch state {
	case core.Paused, core.Failed:
		guard = "1"
	case core.Completed:
		guard = "phase IN ('completing','aborting')"
	case core.Canceled:
		guard = "phase='aborting' AND cancel_requested=1"
	default:
		return core.NewError(core.InvalidTransition)
	}
	return s.uploadTransaction(ctx, func(tx *sql.Tx) error {
		if err := requireMatch(tx.ExecContext(ctx, `UPDATE tasks SET state=? WHERE id=? AND state='running'
            AND EXISTS (SELECT 1 FROM uploads WHERE task_id=tasks.id AND `+guard+")", state, id)); err != nil {
			return err
		}
		var encoded any
		if detail != nil {
			b, err := json.Marshal(detail)
			if err != nil {
				return dbError(err)
			}
			encoded = string(b)
		}
		_, err := tx.ExecContext(ctx, `UPDATE uploads SET last_error=?,
            cancel_requested=CASE WHEN ? IN ('completed','canceled') THEN 0 ELSE cancel_requested END
            WHERE task_id=?`, encoded, state, id)
		if err != nil {
			return dbError(err)
		}
		if state == core.Canceled {
			_, err = tx.ExecContext(ctx, "DELETE FROM parts WHERE task_id=?", id)
			if err != nil {
				return dbError(err)
			}
		}
		return nil
	})
}
