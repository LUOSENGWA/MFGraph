package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"regexp"

	"github.com/LUOSENGWA/MFGraph/internal/actorref"
	"github.com/LUOSENGWA/MFGraph/internal/extref"
	"github.com/LUOSENGWA/MFGraph/internal/ids"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

// Names of the identity headers of module mode, after the configurable
// prefix (for example X-Trusted-Actor with the default prefix X-Trusted-).
// The host platform's proxy sets them on every request it forwards.
const (
	ClaimAudience       = "Audience"
	ClaimActorType      = "Actor-Type"
	ClaimActor          = "Actor"
	ClaimOnBehalfOf     = "On-Behalf-Of"
	ClaimOnBehalfOfType = "On-Behalf-Of-Type"
	ClaimTeam           = "Team"
	ClaimProject        = "Project"
	ClaimRequestID      = "Request-Id"
	ClaimTraceID        = "Trace-Id"
	ClaimTokenJTI       = "Token-Jti"
	ClaimIssuedAt       = "Issued-At"
	ClaimInternalToken  = "Internal-Token"
)

// actorTypeRe is the form of an actor type.
var actorTypeRe = regexp.MustCompile(`^[a-z0-9_-]{1,16}$`)

// authError is an authentication failure with the message the client gets
// (the 401 body). It never contains a header value. claim marks a refused
// identity header of a request that carried the right internal token: the
// host platform sent something this server does not accept, which is worth
// a warning in the log.
type authError struct {
	msg   string
	claim bool
}

func (e *authError) Error() string { return e.msg }

// Unwrap makes errors.Is(err, ports.ErrUnauthenticated) true.
func (e *authError) Unwrap() error { return ports.ErrUnauthenticated }

// RequestClaims are the identity headers that are only recorded: they go to
// the request context and the access log and never decide anything. Values
// that are not valid external references (internal/extref) are dropped.
type RequestClaims struct {
	Audience string
	Team     string
	TokenJTI string
	IssuedAt string
}

// TrustedHeaders authenticates the requests a host platform's proxy
// forwards to a module process (module mode). The proxy authenticates the
// caller itself and passes the result in headers named <prefix><claim>,
// together with an internal token that proves the request comes from it:
//
//  1. the token header must carry the internal token (compared in constant
//     time; only its SHA-256 is kept);
//  2. <prefix>Actor must be an actor reference (internal/actorref: valid
//     UTF-8 of at most 255 bytes without control characters);
//  3. <prefix>Actor-Type must match [a-z0-9_-]{1,16};
//  4. <prefix>Project must be a ULID: the project the request is admitted
//     to (Principal.ProjectScope).
//
// <prefix>On-Behalf-Of and <prefix>On-Behalf-Of-Type are optional and
// checked by the same rules when present (a type without the actor is
// refused; the actor without a type has no type). Team, Token-Jti,
// Issued-At and Audience are only recorded. A header given more than once,
// or a required one that is missing or empty, fails the request. Every
// failure is 401 unauthenticated. Authorization headers are ignored.
type TrustedHeaders struct {
	prefix      string
	tokenHeader string
	token       [sha256.Size]byte
}

// NewTrustedHeaders returns the authenticator for the header prefix, the
// name of the internal token header and the token.
func NewTrustedHeaders(prefix, tokenHeader, token string) (*TrustedHeaders, error) {
	if prefix == "" || tokenHeader == "" {
		return nil, errors.New("api: trusted headers need a prefix and a token header")
	}
	if token == "" {
		return nil, errors.New("api: trusted headers need an internal token")
	}
	return &TrustedHeaders{prefix: prefix, tokenHeader: tokenHeader, token: sha256.Sum256([]byte(token))}, nil
}

var _ ports.Authenticator = (*TrustedHeaders)(nil)

// Authenticate implements ports.Authenticator.
func (a *TrustedHeaders) Authenticate(r *http.Request) (ports.Principal, error) {
	tokens := r.Header.Values(a.tokenHeader)
	if len(tokens) != 1 {
		return ports.Principal{}, &authError{msg: "missing or invalid internal token"}
	}
	got := sha256.Sum256([]byte(tokens[0]))
	if subtle.ConstantTimeCompare(got[:], a.token[:]) != 1 {
		return ports.Principal{}, &authError{msg: "missing or invalid internal token"}
	}
	var p ports.Principal
	var err error
	if p.Ref, err = a.required(r, ClaimActor, actorref.Validate); err != nil {
		return ports.Principal{}, err
	}
	if p.ActorType, err = a.required(r, ClaimActorType, checkActorType); err != nil {
		return ports.Principal{}, err
	}
	if p.ProjectScope, err = a.required(r, ClaimProject, checkULID); err != nil {
		return ports.Principal{}, err
	}
	if p.OnBehalfOfRef, err = a.optional(r, ClaimOnBehalfOf, actorref.Validate); err != nil {
		return ports.Principal{}, err
	}
	if p.OnBehalfOfType, err = a.optional(r, ClaimOnBehalfOfType, checkActorType); err != nil {
		return ports.Principal{}, err
	}
	if p.OnBehalfOfType != "" && p.OnBehalfOfRef == "" {
		return ports.Principal{}, a.invalid(ClaimOnBehalfOfType, "given without "+a.prefix+ClaimOnBehalfOf)
	}
	p.Team = a.recorded(r, ClaimTeam)
	return p, nil
}

// Claims returns the headers of r that are only recorded.
func (a *TrustedHeaders) Claims(r *http.Request) RequestClaims {
	return RequestClaims{Audience: a.recorded(r, ClaimAudience), Team: a.recorded(r, ClaimTeam),
		TokenJTI: a.recorded(r, ClaimTokenJTI), IssuedAt: a.recorded(r, ClaimIssuedAt)}
}

func (a *TrustedHeaders) invalid(claim, why string) error {
	return &authError{msg: fmt.Sprintf("invalid identity header %s%s: %s", a.prefix, claim, why), claim: true}
}

// single returns the value of a header given at most once ("" when absent).
func (a *TrustedHeaders) single(r *http.Request, claim string) (string, error) {
	vs := r.Header.Values(a.prefix + claim)
	switch len(vs) {
	case 0:
		return "", nil
	case 1:
		return vs[0], nil
	}
	return "", a.invalid(claim, "given more than once")
}

func (a *TrustedHeaders) required(r *http.Request, claim string, check func(string) error) (string, error) {
	v, err := a.single(r, claim)
	if err != nil {
		return "", err
	}
	if v == "" {
		return "", a.invalid(claim, "missing")
	}
	if err := check(v); err != nil {
		return "", a.invalid(claim, err.Error())
	}
	return v, nil
}

// optional is required for a header that may be absent or empty.
func (a *TrustedHeaders) optional(r *http.Request, claim string, check func(string) error) (string, error) {
	v, err := a.single(r, claim)
	if err != nil || v == "" {
		return "", err
	}
	if err := check(v); err != nil {
		return "", a.invalid(claim, err.Error())
	}
	return v, nil
}

// recorded returns a header that is only recorded, or "" when it is
// absent, given more than once or not a valid external reference.
func (a *TrustedHeaders) recorded(r *http.Request, claim string) string {
	vs := r.Header.Values(a.prefix + claim)
	if len(vs) != 1 || !extref.Valid(vs[0]) {
		return ""
	}
	return vs[0]
}

func checkActorType(v string) error {
	if !actorTypeRe.MatchString(v) {
		return errors.New("must match [a-z0-9_-]{1,16}")
	}
	return nil
}

func checkULID(v string) error {
	if !ids.Valid(v) {
		return errors.New("must be a ULID (26 uppercase Crockford base32 characters)")
	}
	return nil
}
