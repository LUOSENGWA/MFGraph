package api

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"

	"github.com/LUOSENGWA/MFGraph/internal/actorref"
	"github.com/LUOSENGWA/MFGraph/internal/ids"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

// StaticTokens authenticates "Authorization: Bearer <token>" against a fixed
// token -> actor reference map from the configuration (first core slice).
// Tokens are kept only as SHA-256 digests, so lookups do not compare secret
// strings directly.
type StaticTokens struct {
	byDigest map[[sha256.Size]byte]ports.Principal
}

// NewStaticTokens builds the authenticator. Every token maps to an actor
// reference (internal/actorref), for example "alice" or a ULID. In a
// standalone deployment a reference that is a ULID is the principal_uid of a
// kernel principal, so it is also returned as Principal.PrincipalUID.
func NewStaticTokens(tokens map[string]string) (*StaticTokens, error) {
	s := &StaticTokens{byDigest: make(map[[sha256.Size]byte]ports.Principal, len(tokens))}
	for tok, ref := range tokens {
		if tok == "" {
			return nil, fmt.Errorf("api: empty static token")
		}
		if err := actorref.Validate(ref); err != nil {
			return nil, fmt.Errorf("api: static token entry for actor %q: %w", ref, err)
		}
		p := ports.Principal{Ref: ref}
		if ids.Valid(ref) {
			p.PrincipalUID = ref
		}
		s.byDigest[sha256.Sum256([]byte(tok))] = p
	}
	return s, nil
}

// Authenticate implements ports.Authenticator.
func (s *StaticTokens) Authenticate(r *http.Request) (ports.Principal, error) {
	tok, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		return ports.Principal{}, ports.ErrUnauthenticated
	}
	p, ok := s.byDigest[sha256.Sum256([]byte(tok))]
	if !ok {
		return ports.Principal{}, ports.ErrUnauthenticated
	}
	return p, nil
}

// bearerToken extracts the token of "Bearer <token>" (scheme
// case-insensitive, exactly one token).
func bearerToken(h string) (string, bool) {
	scheme, tok, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	tok = strings.TrimSpace(tok)
	if tok == "" || strings.ContainsAny(tok, " \t") {
		return "", false
	}
	return tok, true
}
