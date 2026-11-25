// Package core defines storage-client contracts without depending on a cloud SDK or desktop runtime.
package core

import (
	"net/url"
	"strings"
)

type Provider string

const (
	AWS          Provider = "aws"
	R2           Provider = "r2"
	OSS          Provider = "oss"
	TOS          Provider = "tos"
	S3Compatible Provider = "s3-compatible"
)

// Connection holds only public configuration. Future adapters obtain credentials from an OS vault.
type Connection struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Provider Provider `json:"provider"`
	Region   string   `json:"region"`
	Endpoint string   `json:"endpoint"`
}

func (c Connection) Validate() error {
	if strings.TrimSpace(c.ID) == "" || strings.TrimSpace(c.Name) == "" {
		return NewError(InvalidConnection)
	}
	switch c.Provider {
	case AWS, R2, OSS, TOS, S3Compatible:
	default:
		return NewError(InvalidConnection)
	}
	if c.Endpoint != "" {
		u, err := url.Parse(c.Endpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
			u.User != nil || strings.ContainsAny(c.Endpoint, "?#") || (u.EscapedPath() != "" && u.EscapedPath() != "/") {
			return NewError(InvalidConnection)
		}
	}
	return nil
}

type Capabilities struct {
	ListBuckets bool `json:"listBuckets"`
	ListObjects bool `json:"listObjects"`
	Upload      bool `json:"upload"`
	Download    bool `json:"download"`
	ResumeParts bool `json:"resumeParts"`
}

// CapabilitiesFor reports only operations backed by a working adapter.
func CapabilitiesFor(provider Provider) Capabilities {
	if provider == AWS || provider == R2 {
		return Capabilities{ListBuckets: true, ListObjects: true, Upload: true, Download: true, ResumeParts: true}
	}
	return Capabilities{}
}

type SpeedMode string

const (
	FullSpeed SpeedMode = "full"
	Office    SpeedMode = "office"
	Custom    SpeedMode = "custom"
)

// TransferOptions describes application-wide byte/s budgets shared by all transfers.
// Zero means unlimited. M0 stores no active scheduler or limiter.
type TransferOptions struct {
	Mode                   SpeedMode `json:"mode"`
	UploadBytesPerSecond   int64     `json:"uploadBytesPerSecond"`
	DownloadBytesPerSecond int64     `json:"downloadBytesPerSecond"`
}

type Direction string

const (
	Upload   Direction = "upload"
	Download Direction = "download"
)

type TaskState string

const (
	Queued    TaskState = "queued"
	Running   TaskState = "running"
	Paused    TaskState = "paused"
	Failed    TaskState = "failed"
	Completed TaskState = "completed"
	Canceled  TaskState = "canceled"
)

func (from TaskState) CanTransition(to TaskState) bool {
	switch from {
	case Queued:
		return to == Running || to == Paused || to == Canceled
	case Running:
		return to == Paused || to == Failed || to == Completed || to == Canceled
	case Paused, Failed:
		return to == Queued || to == Canceled
	default:
		return false
	}
}

type Task struct {
	ID           string    `json:"id"`
	ConnectionID string    `json:"connectionId"`
	Direction    Direction `json:"direction"`
	Bucket       string    `json:"bucket"`
	// ObjectKey is opaque, including repeated slashes and dot segments.
	ObjectKey string    `json:"objectKey"`
	LocalPath string    `json:"localPath"`
	Size      int64     `json:"size"`
	State     TaskState `json:"state"`
	// UploadID is opaque provider recovery metadata and stays in the engine.
	UploadID         string `json:"-"`
	TransferredBytes int64  `json:"transferredBytes"`
	LastError        *Error `json:"lastError,omitempty"`
	Resumable        bool   `json:"resumable"`
	UploadPhase      string `json:"uploadPhase,omitempty"`
	CancelRequested  bool   `json:"cancelRequested,omitempty"`
}

func (t Task) ValidateNew() error {
	if strings.TrimSpace(t.ID) == "" || strings.TrimSpace(t.ConnectionID) == "" ||
		t.Direction != Upload && t.Direction != Download || t.Bucket == "" ||
		t.ObjectKey == "" || t.Size < 0 || t.State != Queued || t.UploadID != "" {
		return NewError(InvalidTask)
	}
	return nil
}

// Part is a durable checkpoint for a specific multipart upload or completed download range.
type Part struct {
	Number   int
	Offset   int64
	Size     int64
	ETag     string
	Checksum string
}
