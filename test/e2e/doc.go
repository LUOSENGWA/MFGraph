// Package e2e holds the black-box acceptance tests of the first core slice.
// They build and launch the real mfgraph-server binary and talk to it over
// HTTP only. They are compiled only with the "e2e" build tag:
//
//	make test-e2e        # go test -tags e2e ./test/e2e/...
//
// While the business endpoints are stubs, every test is expected to fail
// with 501 not_implemented. See README.md.
package e2e
