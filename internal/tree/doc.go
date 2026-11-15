// Package tree implements the frozen manifest tree format: deterministic
// canonical JSON nodes (dir, dir_page, dir_index) addressed by the SHA-256 of
// their uncompressed canonical bytes.
//
// Build constructs a whole tree from a set of files, and Walk verifies a
// whole tree (including the canonical paging of every directory); both are
// meant for imports, debugging and audits. The server's request paths use
// the functions that read only the nodes they need, each verified against
// its hash, with bounded depth: Lookup and LookupPath (find an entry),
// ListDir (one page of a directory), Diff (changed files in path order) and
// Count, and for writes Apply (the changes of a commit, rewriting only the
// directories on their paths) and Graft (restore one path from another
// tree). Apply produces exactly the tree Build would produce for the
// resulting files.
package tree
