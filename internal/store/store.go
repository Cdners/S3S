// Package store implements the SQLite boundary. Acknowledged writes survive a reopen.
// Scheduling, checking remote parts and validating local file identity belong to the transfer engine.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/Cdners/S3S/internal/core"
	"github.com/mattn/go-sqlite3"
)

//go:embed migrations/001.sql
var schema1 string

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath // drive letter on Windows
	}
	uri := url.URL{Scheme: "file", Path: uriPath, RawQuery: "_busy_timeout=5000&_foreign_keys=on&_journal_mode=WAL&_synchronous=FULL&_txlock=immediate"}
	db, err := sql.Open("sqlite3", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func migrate(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	switch version {
	case 0:
		if _, err := tx.Exec(schema1); err != nil {
			return err
		}
		if _, err := tx.Exec("PRAGMA user_version = 1"); err != nil {
			return err
		}
	case 1:
		// Already current.
	default:
		return errors.New("unsupported S3S database schema version")
	}
	return tx.Commit()
}

func (s *Store) Close() error { return s.db.Close() }

// Map database errors before exposing them to a desktop caller. SQL text or paths
// are not safe to display and are never used as message keys.
func dbError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return core.NewError(core.NotFound)
	}
	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code == sqlite3.ErrConstraint {
		return core.NewError(core.Conflict)
	}
	return core.NewError(core.Storage)
}

func (s *Store) SaveConnection(ctx context.Context, c core.Connection) error {
	if err := c.Validate(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO connections (id,name,provider,region,endpoint) VALUES (?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name,provider=excluded.provider,region=excluded.region,endpoint=excluded.endpoint`,
		c.ID, c.Name, c.Provider, c.Region, c.Endpoint)
	return dbError(err)
}

func (s *Store) Connections(ctx context.Context) ([]core.Connection, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,name,provider,region,endpoint FROM connections ORDER BY name,id")
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	list := []core.Connection{}
	for rows.Next() {
		var c core.Connection
		if err := rows.Scan(&c.ID, &c.Name, &c.Provider, &c.Region, &c.Endpoint); err != nil {
			return nil, dbError(err)
		}
		list = append(list, c)
	}
	return list, dbError(rows.Err())
}

func (s *Store) CreateTask(ctx context.Context, t core.Task) error {
	if err := t.ValidateNew(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO tasks (id,connection_id,direction,bucket,object_key,local_path,size,state)
		VALUES (?,?,?,?,?,?,?,?)`, t.ID, t.ConnectionID, t.Direction, t.Bucket, t.ObjectKey, t.LocalPath, t.Size, t.State)
	return dbError(err)
}

func (s *Store) Tasks(ctx context.Context) ([]core.Task, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,connection_id,direction,bucket,object_key,local_path,size,state,upload_id FROM tasks ORDER BY rowid`)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	list := []core.Task{}
	for rows.Next() {
		var t core.Task
		if err := rows.Scan(&t.ID, &t.ConnectionID, &t.Direction, &t.Bucket, &t.ObjectKey, &t.LocalPath, &t.Size, &t.State, &t.UploadID); err != nil {
			return nil, dbError(err)
		}
		list = append(list, t)
	}
	return list, dbError(rows.Err())
}

func requireMatch(result sql.Result, err error) error {
	if err != nil {
		return dbError(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return dbError(err)
	}
	if count != 1 {
		return core.NewError(core.Conflict)
	}
	return nil
}

// Transition uses compare-and-swap so delayed workers cannot overwrite newer state.
func (s *Store) Transition(ctx context.Context, id string, from, to core.TaskState) error {
	if !from.CanTransition(to) {
		return core.NewError(core.InvalidTransition)
	}
	return requireMatch(s.db.ExecContext(ctx, "UPDATE tasks SET state=? WHERE id=? AND state=?", to, id, from))
}

// BindUpload records the provider's multipart session before any part can be checkpointed.
func (s *Store) BindUpload(ctx context.Context, id, uploadID string) error {
	if uploadID == "" {
		return core.NewError(core.InvalidTask)
	}
	return requireMatch(s.db.ExecContext(ctx, `UPDATE tasks SET upload_id=? WHERE id=? AND direction='upload'
		AND state='running' AND upload_id IN ('',?)`, uploadID, id, uploadID))
}

// Checkpoint records acknowledged bytes. Identical writes are safe to retry.
// A different part cannot claim the same byte range, even with concurrent callers.
func (s *Store) Checkpoint(ctx context.Context, id string, p core.Part) error {
	if p.Number <= 0 || p.Size <= 0 || p.Offset < 0 {
		return core.NewError(core.InvalidPart)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer tx.Rollback()
	var size int64
	var state core.TaskState
	var direction core.Direction
	var uploadID string
	if err := tx.QueryRowContext(ctx, "SELECT size,state,direction,upload_id FROM tasks WHERE id=?", id).Scan(&size, &state, &direction, &uploadID); err != nil {
		return dbError(err)
	}
	if state != core.Running || direction == core.Upload && uploadID == "" {
		return core.NewError(core.Conflict)
	}
	if p.Size > size || p.Offset > size-p.Size {
		return core.NewError(core.InvalidPart)
	}
	var old core.Part
	err = tx.QueryRowContext(ctx, `SELECT number,offset,size,etag,checksum FROM parts WHERE task_id=?
		AND (number=? OR (offset < ? AND offset+size > ?)) LIMIT 1`, id, p.Number, p.Offset+p.Size, p.Offset).
		Scan(&old.Number, &old.Offset, &old.Size, &old.ETag, &old.Checksum)
	switch {
	case err == nil:
		if old != p {
			return core.NewError(core.Conflict)
		}
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx, "INSERT INTO parts(task_id,number,offset,size,etag,checksum) VALUES (?,?,?,?,?,?)",
			id, p.Number, p.Offset, p.Size, p.ETag, p.Checksum)
		if err != nil {
			return dbError(err)
		}
	default:
		return dbError(err)
	}
	return dbError(tx.Commit())
}

func (s *Store) Parts(ctx context.Context, id string) ([]core.Part, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT number,offset,size,etag,checksum FROM parts WHERE task_id=? ORDER BY number", id)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	list := []core.Part{}
	for rows.Next() {
		var p core.Part
		if err := rows.Scan(&p.Number, &p.Offset, &p.Size, &p.ETag, &p.Checksum); err != nil {
			return nil, dbError(err)
		}
		list = append(list, p)
	}
	return list, dbError(rows.Err())
}
