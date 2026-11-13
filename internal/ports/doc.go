// Package ports declares the interfaces the core depends on. Adapters
// (storage backends, SQL repositories, HTTP authentication) implement them;
// package ports/memory provides in-memory implementations for tests.
//
//   - BlobStore: immutable objects under full keys, with the durability rule
//     for POSIX written on the interface; TreeStore: canonical tree nodes per
//     workspace.
//   - Authenticator (request -> Principal) and Authorizer (principal, action,
//     resource -> nil / ErrForbidden / ErrNotFound).
//   - Clock and IDGen.
//   - Transactor, Tx and EventSink: outbox events are appended in the same
//     transaction as the business writes.
//
// The package depends only on the standard library and contenthash.
package ports
