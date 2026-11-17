// Package core holds the use cases of the first core slice (M1.0a):
// creating projects and workspaces, staging objects, committing changes,
// browsing, downloading, diffing and restoring snapshots (Service).
//
// Dependency rule: core may import only the standard library, the ports
// package and the pure format packages (contenthash, pathx, tree,
// storagekey). Adapters (HTTP, SQL, storage backends) depend on core, never
// the other way round. The rule is enforced by deps_test.go.
//
// Errors are *Error values with the stable codes of the API (Code*) and
// details (reason, current_generation, hashes, ...), or *pathx.Error for
// paths that break a rule (invalid_path).
//
// Every change writes its events (events.go, docs/events.md) to the outbox
// in its own transaction. With Config.HostOwnsProjects (module mode) the
// host platform owns the projects: the core never reads the projects
// table and creates workspaces in the project of the request.
package core
