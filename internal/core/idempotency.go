package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

// Routes of the idempotency records: the operation, without the
// configurable API prefix. The path parameters are part of the request
// hash.
const (
	RouteCreateProject   = "POST /projects"
	RouteCreateWorkspace = "POST /workspaces"
	RouteCommit          = "POST /workspaces/{workspace_id}/commits"
	RouteRestore         = "POST /snapshots/{snapshot_id}/restore"
)

// checkIdempotencyKey: 1-128 visible ASCII characters (contract).
func checkIdempotencyKey(k string) error {
	if len(k) > MaxIdempotencyKey {
		return invalid(fmt.Sprintf("Idempotency-Key has %d characters, at most %d", len(k), MaxIdempotencyKey))
	}
	for i := 0; i < len(k); i++ {
		if k[i] < 0x21 || k[i] > 0x7e {
			return invalid("Idempotency-Key must consist of visible ASCII characters")
		}
	}
	return nil
}

// requestHash is the SHA-256 of the normalized request: the JSON encoding
// of the decoded request (fixed field order) and its path parameters.
func requestHash(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// completer stores the response of an idempotent request in the
// transaction of the business change; nil when the request has no key.
type completer func(ctx context.Context, tx ports.Tx, response any) error

// idempotent runs an operation under an Idempotency-Key (contract,
// "Idempotency"): the record is identified by actor reference, route and
// key and carries the hash of the request.
//
//   - A completed record with the same request hash: its response is
//     returned again, nothing is executed.
//   - A record with another request hash: idempotency_key_reuse.
//   - An in-progress record (the same request still running, or a request
//     whose server stopped): conflict with reason in_progress.
//   - Otherwise an in-progress record is inserted (in its own
//     transaction), and run is executed. run must call complete with its
//     response in the transaction of its business change, which turns the
//     record into a completed one; if run fails, the record is deleted.
//     Expired records are replaced.
//
// Without a key, run is executed with a nil completer.
func idempotent[T any](ctx context.Context, s *Service, p ports.Principal, route, key string, request any,
	run func(ctx context.Context, complete completer) (T, error)) (T, error) {
	var zero T
	if key == "" {
		return run(ctx, nil)
	}
	if err := checkIdempotencyKey(key); err != nil {
		return zero, err
	}
	h, err := requestHash(request)
	if err != nil {
		return zero, err
	}
	now := s.nowUs()
	rec := ports.IdempotencyRecord{ActorRef: p.Ref, Route: route, Key: key, RequestHash: h, Status: ports.IdempotencyInProgress,
		CreatedAtUs: now, ExpiresAtUs: now + s.cfg.IdempotencyTTL.Microseconds()}
	for attempt := 0; ; attempt++ {
		err := s.d.Store.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
			return s.d.Store.InsertIdempotency(ctx, tx, rec)
		})
		if err == nil {
			break
		}
		if !errors.Is(err, ports.ErrAlreadyExists) || attempt == 3 {
			return zero, mapErr(err, "idempotency record")
		}
		old, err := s.d.Store.GetIdempotency(ctx, nil, p.Ref, route, key)
		switch {
		case errors.Is(err, ports.ErrNotFound):
			continue // deleted meanwhile
		case err != nil:
			return zero, mapErr(err, "idempotency record")
		case old.ExpiresAtUs <= now:
			if err := s.d.Store.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
				return s.d.Store.DeleteIdempotency(ctx, tx, p.Ref, route, key, old.CreatedAtUs, old.Status)
			}); err != nil {
				return zero, mapErr(err, "idempotency record")
			}
			continue
		case old.RequestHash != h:
			return zero, newError(CodeIdempotencyKeyReuse, "the Idempotency-Key was used for a different request", nil)
		case old.Status != ports.IdempotencyCompleted:
			return zero, newError(CodeConflict, "a request with this Idempotency-Key is in progress",
				map[string]any{"reason": ReasonInProgress})
		}
		var v T
		if err := json.Unmarshal(old.Response, &v); err != nil {
			return zero, fmt.Errorf("core: stored idempotent response: %w", err)
		}
		return v, nil
	}
	complete := func(ctx context.Context, tx ports.Tx, response any) error {
		b, err := json.Marshal(response)
		if err != nil {
			return err
		}
		if err := s.d.Store.CompleteIdempotency(ctx, tx, p.Ref, route, key, rec.CreatedAtUs, b); err != nil {
			if errors.Is(err, ports.ErrNotFound) {
				return newError(CodeConflict, "the idempotency record of this request was replaced", nil)
			}
			return mapErr(err, "idempotency record")
		}
		return nil
	}
	v, err := run(ctx, complete)
	if err != nil {
		// The business change did not commit, so the record is still in
		// progress: remove it, so that the request can be retried.
		cleanup := context.WithoutCancel(ctx)
		_ = s.d.Store.WithTx(cleanup, func(ctx context.Context, tx ports.Tx) error {
			return s.d.Store.DeleteIdempotency(ctx, tx, p.Ref, route, key, rec.CreatedAtUs, ports.IdempotencyInProgress)
		})
		return zero, err
	}
	return v, nil
}

// completeIf calls complete when the request has a key.
func completeIf(ctx context.Context, complete completer, tx ports.Tx, response any) error {
	if complete == nil {
		return nil
	}
	return complete(ctx, tx, response)
}
