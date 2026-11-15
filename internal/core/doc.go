// Package core will hold the use cases of the first core slice (M1.0a):
// creating workspaces, staging objects, committing changes, browsing, diffing
// and restoring snapshots.
//
// Dependency rule: core may import only the standard library, the ports
// package and the pure format packages (contenthash, pathx, tree,
// storagekey). Adapters (HTTP, SQL, storage backends) depend on core, never
// the other way round. The rule is enforced by deps_test.go.
//
// M0 intentionally contains no business logic here.
package core
