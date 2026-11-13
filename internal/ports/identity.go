package ports

import (
	"errors"
	"net/http"
)

var (
	// ErrUnauthenticated: no or invalid credentials.
	ErrUnauthenticated = errors.New("unauthenticated")
	// ErrNotFound: the resource does not exist or is not visible to the
	// caller. Authorization failures on resources the caller cannot see use
	// ErrNotFound too, so that existence is not revealed.
	ErrNotFound = errors.New("not found")
	// ErrForbidden: the caller can see the resource but may not perform the
	// action.
	ErrForbidden = errors.New("forbidden")
)

// Principal is an authenticated identity: a human, an agent, a service or a
// team are all principals and are treated alike.
type Principal struct {
	UID      string // principal_uid (ULID)
	TenantID string // empty when the authenticator does not know it
}

// Authenticator turns request credentials into a Principal. It returns
// ErrUnauthenticated (possibly wrapped) when the request carries no valid
// credentials.
type Authenticator interface {
	Authenticate(r *http.Request) (Principal, error)
}

// IDGen generates new unique IDs (ULIDs in production).
type IDGen interface {
	NewID() string
}
