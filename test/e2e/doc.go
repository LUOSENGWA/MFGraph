// Package e2e holds the black-box acceptance tests of the first core slice.
// They build and launch the real mfgraph-server binary and talk to it over
// HTTP only. They are compiled only with the "e2e" build tag and need a
// database (MFGRAPH_E2E_DB_DSN, MySQL 8.0 or seekdb); without one every test
// is skipped with the reason:
//
//	MFGRAPH_E2E_DB_DSN='root:mfgraph@tcp(127.0.0.1:3306)/' make test-e2e
//
// See README.md.
package e2e
