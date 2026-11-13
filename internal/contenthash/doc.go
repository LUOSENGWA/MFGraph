// Package contenthash implements content identity: the whole-file SHA-256 of
// the exact bytes, written as "sha256:<64 lowercase hex digits>".
//
// Content identity never depends on chunking, part sizes, storage backends or
// upload paths. Parsing is strict so that every hash has exactly one textual
// representation.
package contenthash
