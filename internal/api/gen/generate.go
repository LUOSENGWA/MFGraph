// Package gen contains the code generated from contracts/openapi.yaml: the
// models, the net/http server interface with its strict variant
// (server.gen.go) and the HTTP client of the CLI (client.gen.go). Do not
// edit the generated files; change the contract and run `go generate`.
package gen

//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.2 -config oapi-codegen.yaml ../../../contracts/openapi.yaml
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.2 -config oapi-codegen-client.yaml ../../../contracts/openapi.yaml
