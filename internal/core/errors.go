package core

type ErrorCode string

const (
	InvalidConnection ErrorCode = "error.invalidConnection"
	InvalidTask       ErrorCode = "error.invalidTask"
	InvalidTransition ErrorCode = "error.invalidTransition"
	InvalidPart       ErrorCode = "error.invalidPart"
	NotFound          ErrorCode = "error.notFound"
	Conflict          ErrorCode = "error.conflict"
	Storage           ErrorCode = "error.storage"
)

// Error carries a message key and safe display metadata. Provider response bodies,
// headers, credentials and request URLs must never enter this boundary.
type Error struct {
	Code         ErrorCode         `json:"code"`
	Params       map[string]string `json:"params,omitempty"`
	ProviderCode string            `json:"providerCode,omitempty"`
	RequestID    string            `json:"requestId,omitempty"`
	Retryable    bool              `json:"retryable"`
}

func NewError(code ErrorCode) *Error { return &Error{Code: code} }
func (e *Error) Error() string       { return string(e.Code) }
