package api_test

import (
	"context"
	"go/ast"
	"go/parser"
	gotoken "go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

const specPath = "../../contracts/openapi.yaml"

func loadSpec(t *testing.T) *openapi3.T {
	t.Helper()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile(filepath.FromSlash(specPath))
	if err != nil {
		t.Fatalf("load %s: %v", specPath, err)
	}
	return doc
}

// TestOpenAPIValid validates the contract with kin-openapi (structure,
// references, schema examples and defaults).
func TestOpenAPIValid(t *testing.T) {
	doc := loadSpec(t)
	if doc.OpenAPI != "3.0.3" {
		t.Fatalf("openapi version %q, want 3.0.3", doc.OpenAPI)
	}
	if err := doc.Validate(context.Background(), openapi3.EnableExamplesValidation()); err != nil {
		t.Fatalf("validation: %v", err)
	}
}

// operations of the first core slice; adding or removing one is a contract
// change and must be reflected here.
var wantOperations = []string{
	"GET /healthz", "GET /readyz",
	"POST /api/v1/projects", "GET /api/v1/projects/{project_id}",
	"POST /api/v1/workspaces", "GET /api/v1/workspaces/{workspace_id}",
	"GET /api/v1/workspaces/{workspace_id}/branches", "PUT /api/v1/workspaces/{workspace_id}/objects",
	"PUT /api/v1/objects/{workspace_id}",
	"POST /api/v1/workspaces/{workspace_id}/commits", "GET /api/v1/workspaces/{workspace_id}/timeline",
	"GET /api/v1/snapshots/{snapshot_id}", "GET /api/v1/snapshots/{snapshot_id}/tree",
	"GET /api/v1/snapshots/{snapshot_id}/file", "GET /api/v1/snapshots/{snapshot_id}/diff",
	"POST /api/v1/snapshots/{snapshot_id}/restore",
}

// TestOpenAPIConventions enforces the global conventions on every operation.
func TestOpenAPIConventions(t *testing.T) {
	doc := loadSpec(t)
	var got []string
	ids := map[string]bool{}
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			key := method + " " + path
			got = append(got, key)
			if op.OperationID == "" || ids[op.OperationID] {
				t.Errorf("%s: missing or duplicate operationId %q", key, op.OperationID)
			}
			ids[op.OperationID] = true
			health := path == "/healthz" || path == "/readyz"

			// Security: bearer everywhere except health.
			if health {
				if op.Security == nil || len(*op.Security) != 0 {
					t.Errorf("%s: health endpoints must declare security: []", key)
				}
			} else if op.Security != nil {
				t.Errorf("%s: must use the global bearer security", key)
			}

			// Parameters.
			params := map[string]*openapi3.Parameter{}
			for _, p := range op.Parameters {
				params[p.Value.In+":"+p.Value.Name] = p.Value
			}
			if params["header:X-Request-Id"] == nil {
				t.Errorf("%s: missing X-Request-Id parameter", key)
			}
			if method == "POST" && params["header:Idempotency-Key"] == nil {
				t.Errorf("%s: POST without Idempotency-Key", key)
			}
			for _, p := range params {
				if p.In != "path" {
					continue
				}
				if !strings.HasSuffix(p.Name, "_id") || p.Schema == nil || !strings.HasSuffix(p.Schema.Ref, "/ULID") {
					t.Errorf("%s: path parameter %q must be an ID with the ULID schema", key, p.Name)
				}
			}
			if strings.Contains(path, "{path}") {
				t.Errorf("%s: file paths must not appear in URL paths", key)
			}

			// Responses: every error response uses the Error schema, and
			// every operation has a default error response.
			if op.Responses.Default() == nil {
				t.Errorf("%s: missing default response", key)
			}
			if !health && op.Responses.Value("401") == nil {
				t.Errorf("%s: missing 401", key)
			}
			for code, resp := range op.Responses.Map() {
				if code == "default" || code[0] == '4' || code[0] == '5' {
					mt := resp.Value.Content.Get("application/json")
					if mt == nil || mt.Schema == nil || !strings.HasSuffix(mt.Schema.Ref, "/Error") {
						t.Errorf("%s %s: error responses must be application/json Error", key, code)
					}
				}
			}
			if op.Description == "" && !health && !strings.Contains(op.Summary, "") {
				t.Errorf("%s: missing description", key)
			}
			if !health && !strings.Contains(op.Description, "Errors:") {
				t.Errorf("%s: description must list the possible error codes", key)
			}
		}
	}
	sort.Strings(got)
	want := append([]string(nil), wantOperations...)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("operations:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// The Error schema is the flat envelope.
	es := doc.Components.Schemas["Error"].Value
	for _, f := range []string{"code", "message", "request_id", "details"} {
		if es.Properties[f] == nil {
			t.Errorf("Error schema lacks %q", f)
		}
	}
	if strings.Join(es.Required, ",") != "code,message" {
		t.Errorf("Error.required = %v", es.Required)
	}

	// List responses: {items, next_cursor}.
	for _, name := range []string{"BranchList", "SnapshotList", "TreeListing", "Diff"} {
		s := doc.Components.Schemas[name].Value
		if s.Properties["items"] == nil || s.Properties["next_cursor"] == nil || !s.Properties["next_cursor"].Value.Nullable {
			t.Errorf("%s must have items and nullable next_cursor", name)
		}
	}
	lim := doc.Components.Parameters["Limit"].Value.Schema.Value
	if lim.Default != float64(50) || lim.Max == nil || *lim.Max != 500 {
		t.Errorf("limit default/max = %v/%v", lim.Default, lim.Max)
	}
}

// TestOpenAPIPathErrorKinds: details.kind of invalid_path refers to the
// PathErrorKind enum, and the enum lists exactly the Kind constants of
// internal/pathx (read from its source, so a new kind cannot be forgotten).
func TestOpenAPIPathErrorKinds(t *testing.T) {
	doc := loadSpec(t)
	kind := doc.Components.Schemas["Error"].Value.Properties["details"].Value.Properties["kind"]
	if kind == nil || !strings.HasSuffix(kind.Ref, "/PathErrorKind") {
		t.Fatalf("Error.details.kind must reference PathErrorKind, is %+v", kind)
	}
	var spec []string
	for _, v := range doc.Components.Schemas["PathErrorKind"].Value.Enum {
		spec = append(spec, v.(string))
	}
	f, err := parser.ParseFile(gotoken.NewFileSet(), filepath.Join("..", "pathx", "pathx.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var code []string
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok || vs.Type == nil {
			return true
		}
		if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "Kind" {
			return true
		}
		for _, v := range vs.Values {
			if lit, ok := v.(*ast.BasicLit); ok && lit.Kind == gotoken.STRING {
				s, _ := strconv.Unquote(lit.Value)
				code = append(code, s)
			}
		}
		return true
	})
	sort.Strings(spec)
	sort.Strings(code)
	if len(code) < 16 || strings.Join(spec, ",") != strings.Join(code, ",") {
		t.Errorf("PathErrorKind enum = %v\npathx kinds          = %v", spec, code)
	}
}

// TestOpenAPIActorFields: actors are actor references. Every property named
// *_ref (other than ref_name, a branch ref) uses the ActorRef schema and has
// a nullable *_principal_uid sibling with the ULID pattern; no ULID-typed
// identity field (author_principal_uid alone, created_by, owner_principal_id,
// ...) is required.
func TestOpenAPIActorFields(t *testing.T) {
	doc := loadSpec(t)
	ar := doc.Components.Schemas["ActorRef"]
	if ar == nil || ar.Value.MinLength != 1 || ar.Value.MaxLength == nil || *ar.Value.MaxLength != 255 {
		t.Fatalf("ActorRef must be a string of 1 to 255 bytes: %+v", ar)
	}
	n := 0
	for name, sr := range doc.Components.Schemas {
		props := sr.Value.Properties
		for prop, ps := range props {
			if prop == "created_by" || strings.HasSuffix(prop, "_principal_id") {
				t.Errorf("%s.%s: actors are *_ref (ActorRef) plus *_principal_uid", name, prop)
			}
			if !strings.HasSuffix(prop, "_ref") || prop == "ref_name" {
				continue
			}
			n++
			if !strings.HasSuffix(ps.Ref, "/ActorRef") {
				t.Errorf("%s.%s must reference ActorRef", name, prop)
			}
			uid := props[strings.TrimSuffix(prop, "_ref")+"_principal_uid"]
			if uid == nil || !uid.Value.Nullable || uid.Value.Pattern != doc.Components.Schemas["ULID"].Value.Pattern {
				t.Errorf("%s.%s needs a nullable ULID sibling %s_principal_uid", name, prop, strings.TrimSuffix(prop, "_ref"))
			}
			for _, req := range sr.Value.Required {
				if req == strings.TrimSuffix(prop, "_ref")+"_principal_uid" {
					t.Errorf("%s: %s must be optional", name, req)
				}
			}
		}
	}
	if n == 0 {
		t.Error("no actor fields found")
	}
}
