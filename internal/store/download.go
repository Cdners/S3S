package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/Cdners/S3S/internal/core"
)

// A manifest is committed together with its task, before the first range request.
type Download struct {
	Identity                              core.ObjectIdentity
	Provider                              core.Provider
	Region, Endpoint, PartialPath, FileID string
	RangeSize                             int64
}

func (s *Store) CreateDownload(ctx context.Context, t core.Task, d Download) error {
	if err := t.ValidateNew(); err != nil {
		return err
	}
	if t.Direction != core.Download || t.Size != d.Identity.Size || d.FileID == "" || d.PartialPath == "" || d.RangeSize <= 0 {
		return core.NewError(core.InvalidTask)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO tasks (id,connection_id,direction,bucket,object_key,local_path,size,state)
        VALUES (?,?,?,?,?,?,?,?)`, t.ID, t.ConnectionID, t.Direction, t.Bucket, t.ObjectKey, t.LocalPath, t.Size, t.State)
	if err != nil {
		return dbError(err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO downloads(task_id,provider,region,endpoint,etag,version_id,partial_path,file_id,range_size)
        VALUES(?,?,?,?,?,?,?,?,?)`, t.ID, d.Provider, d.Region, d.Endpoint, d.Identity.ETag, d.Identity.VersionID, d.PartialPath, d.FileID, d.RangeSize)
	if err != nil {
		return dbError(err)
	}
	return dbError(tx.Commit())
}

func (s *Store) Download(ctx context.Context, id string) (Download, error) {
	var d Download
	err := s.db.QueryRowContext(ctx, `SELECT t.size,d.provider,d.region,d.endpoint,d.etag,d.version_id,d.partial_path,d.file_id,d.range_size
        FROM downloads d JOIN tasks t ON t.id=d.task_id WHERE d.task_id=?`, id).
		Scan(&d.Identity.Size, &d.Provider, &d.Region, &d.Endpoint, &d.Identity.ETag, &d.Identity.VersionID, &d.PartialPath, &d.FileID, &d.RangeSize)
	return d, dbError(err)
}

const taskSelect = `SELECT t.id,t.connection_id,t.direction,t.bucket,t.object_key,t.local_path,t.size,t.state,t.upload_id,
    COALESCE((SELECT SUM(p.size) FROM parts p WHERE p.task_id=t.id),0),COALESCE(d.last_error,u.last_error),
    d.task_id IS NOT NULL OR u.task_id IS NOT NULL,COALESCE(u.phase,''),COALESCE(u.cancel_requested,0)
    FROM tasks t LEFT JOIN downloads d ON d.task_id=t.id LEFT JOIN uploads u ON u.task_id=t.id`

func scanTask(row interface{ Scan(...any) error }) (core.Task, error) {
	var t core.Task
	var detail sql.NullString
	if err := row.Scan(&t.ID, &t.ConnectionID, &t.Direction, &t.Bucket, &t.ObjectKey, &t.LocalPath, &t.Size, &t.State, &t.UploadID,
		&t.TransferredBytes, &detail, &t.Resumable, &t.UploadPhase, &t.CancelRequested); err != nil {
		return t, dbError(err)
	}
	if detail.Valid {
		if err := json.Unmarshal([]byte(detail.String), &t.LastError); err != nil {
			return t, core.NewError(core.Storage)
		}
	}
	return t, nil
}

func (s *Store) Task(ctx context.Context, id string) (core.Task, error) {
	return scanTask(s.db.QueryRowContext(ctx, taskSelect+" WHERE t.id=?", id))
}

func (s *Store) Connection(ctx context.Context, id string) (core.Connection, error) {
	var c core.Connection
	err := s.db.QueryRowContext(ctx, `SELECT id,name,provider,region,endpoint FROM connections WHERE id=?`, id).
		Scan(&c.ID, &c.Name, &c.Provider, &c.Region, &c.Endpoint)
	return c, dbError(err)
}

// The caller holds the application-wide DB lock and has not started any workers.
func (s *Store) RecoverDownloads(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET state='paused'
        WHERE state='running' AND id IN (SELECT task_id FROM downloads)`)
	return dbError(err)
}

// ClaimDownload claims one eligible task atomically and clears its previous failure.
func (s *Store) ClaimDownload(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET state='queued' WHERE id=? AND state IN ('paused','failed')`, id); err != nil {
		return dbError(err)
	}
	if err = requireMatch(tx.ExecContext(ctx, `UPDATE tasks SET state='running' WHERE id=?
        AND state='queued' AND id IN (SELECT task_id FROM downloads)`, id)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE downloads SET last_error=NULL WHERE task_id=?", id); err != nil {
		return dbError(err)
	}
	return dbError(tx.Commit())
}

// FinishDownload commits the new state and its safe diagnostic as one operation.
func (s *Store) FinishDownload(ctx context.Context, id string, state core.TaskState, detail *core.Error) error {
	if state != core.Completed && state != core.Paused && state != core.Failed {
		return core.NewError(core.InvalidTransition)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer tx.Rollback()
	if err = requireMatch(tx.ExecContext(ctx, "UPDATE tasks SET state=? WHERE id=? AND state='running'", state, id)); err != nil {
		return err
	}
	var encoded any
	if detail != nil {
		b, err := json.Marshal(detail)
		if err != nil {
			return core.NewError(core.Storage)
		}
		encoded = string(b)
	}
	if err = requireMatch(tx.ExecContext(ctx, "UPDATE downloads SET last_error=? WHERE task_id=?", encoded, id)); err != nil {
		return err
	}
	return dbError(tx.Commit())
}

type CompletedPartial struct{ ID, Path, Destination, FileID string }

func (s *Store) PendingCleanup(ctx context.Context) ([]CompletedPartial, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT t.id,d.partial_path,t.local_path,d.file_id FROM downloads d JOIN tasks t ON d.task_id=t.id
       WHERE t.state='completed' AND d.cleanup_pending=1`)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	var out []CompletedPartial
	for rows.Next() {
		var p CompletedPartial
		if err := rows.Scan(&p.ID, &p.Path, &p.Destination, &p.FileID); err != nil {
			return nil, dbError(err)
		}
		out = append(out, p)
	}
	return out, dbError(rows.Err())
}

func (s *Store) CleanupDone(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE downloads SET cleanup_pending=0 WHERE task_id=?", id)
	return dbError(err)
}

func (s *Store) ReplaceMissingPartial(ctx context.Context, id, oldPath, newPath, newID string) error {
	return requireMatch(s.db.ExecContext(ctx, `UPDATE downloads SET partial_path=?,file_id=? WHERE task_id=? AND partial_path=?
       AND task_id IN (SELECT id FROM tasks WHERE state='running')`, newPath, newID, id, oldPath))
}
