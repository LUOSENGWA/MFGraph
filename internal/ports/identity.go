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
//
// Ref is the actor reference of the principal, the identity string the
// host platform (or the standalone token configuration) passes for it: a
// worker name, a user name, an ID such as "@alice:example.com", or in a
// standalone deployment the string form of the principal's ULID. It is
// non-empty valid UTF-8 of at most 255 bytes without control characters
// (internal/actorref), stored as given and compared byte for byte; it is
// what *_ref columns record and what authorization decides on.
type Principal struct {
	Ref string // actor reference (required)
	// PrincipalUID is the principal's ULID when it is known: in a standalone
	// deployment when Ref is a ULID, or once a host platform provides ULIDs.
	// Empty otherwise; it fills the optional *_principal_uid columns.
	PrincipalUID string
	TenantID     string // empty when the authenticator does not know it
	// ActorType is the kind of actor the host platform names for Ref
	// ([a-z0-9_-]{1,16}, for example "human" or "worker"); empty in a
	// standalone deployment. Actors of different types may have the same
	// Ref, so records that keep an actor keep its type too.
	ActorType string
	// OnBehalfOfRef and OnBehalfOfType name the actor this request acts
	// for, when the host platform says so (an actor reference and an
	// optional type); empty otherwise.
	OnBehalfOfRef  string
	OnBehalfOfType string
	// ProjectScope is the project the host platform admitted this request
	// to (module mode): only resources of this project are visible. Empty
	// in a standalone deployment.
	ProjectScope string
	// Team is the team the host platform names for the request; it is
	// recorded only and never used to decide anything.
	Team string
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
