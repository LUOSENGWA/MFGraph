package api

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"

	"github.com/LUOSENGWA/MFGraph/internal/ids"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

// StaticTokens authenticates "Authorization: Bearer <token>" against a fixed
// token -> principal_uid map from the configuration (first core slice).
// Tokens are kept only as SHA-256 digests, so lookups do not compare secret
// strings directly.
type StaticTokens struct {
	byDigest map[[sha256.Size]byte]ports.Principal
}

// NewStaticTokens builds the authenticator. Principal UIDs must be ULIDs.
func NewStaticTokens(tokens map[string]string) (*StaticTokens, error) {
	s := &StaticTokens{byDigest: make(map[[sha256.Size]byte]ports.Principal, len(tokens))}
	for tok, uid := range tokens {
		if tok == "" || !ids.Valid(uid) {
			return nil, fmt.Errorf("api: invalid static token entry for principal %q", uid)
		}
		s.byDigest[sha256.Sum256([]byte(tok))] = ports.Principal{UID: uid}
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
