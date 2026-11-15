// Package pathx implements the frozen workspace path grammar: server-side NFC
// normalization, segment and length limits, sibling conflict detection
// (case-fold and file/directory name clashes) and branch name validation.
//
// Paths are relative, '/'-separated and never contain '.', '..', empty
// segments, backslashes or control characters. Directories are implicit.
package pathx
