package core

// ObjectIdentity pins a range request to the object observed by Head.
// ETag is an opaque, quoted HTTP validator, not necessarily a content checksum.
type ObjectIdentity struct {
	Size      int64  `json:"size"`
	ETag      string `json:"etag"`
	VersionID string `json:"versionId"`
}

// CloudCredentials are passed only to the selected adapter after checking the
// saved account scope. None of these fields may be marshaled into task or IPC data.
type CloudCredentials struct {
	AccessKeyID     string `json:"-"`
	SecretAccessKey string `json:"-"`
	SessionToken    string `json:"-"`
}

type BucketPage struct {
	Buckets   []string `json:"buckets"`
	NextToken string   `json:"nextToken"`
}

type Object struct {
	Key  string `json:"key"`
	Size int64  `json:"size"`
	ETag string `json:"etag"`
}

type ObjectPage struct {
	Objects   []Object `json:"objects"`
	Prefixes  []string `json:"prefixes"`
	NextToken string   `json:"nextToken"`
}
