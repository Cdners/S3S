package core

// ObjectIdentity pins a range request to the object observed by Head.
// ETag is an opaque, quoted HTTP validator, not necessarily a content checksum.
type ObjectIdentity struct {
	Size      int64  `json:"size"`
	ETag      string `json:"etag"`
	VersionID string `json:"versionId,omitempty"`
}
