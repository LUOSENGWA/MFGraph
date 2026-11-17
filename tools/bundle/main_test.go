package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/LUOSENGWA/MFGraph/migrations"
)

const root = "../.." // the repository, seen from this package

// testInfo is the build information of the tests.
var testInfo = BuildInfo{Version: "0.1.0", Commit: strings.Repeat("ab", 20), GoVersion: "go1.26.1", Epoch: defaultEpoch}

func testEntries(t *testing.T) []Entry {
	t.Helper()
	es, err := Entries(root, testInfo, []byte("\x7fELF fake server"))
	if err != nil {
		t.Fatal(err)
	}
	return es
}

type header struct {
	name         string
	mode         int64
	typ          byte
	uid, gid     int
	uname, gname string
	mtime        time.Time
	format       tar.Format
	data         []byte
}

func readArchive(t *testing.T, b []byte) []header {
	t.Helper()
	zr, err := zstd.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	var out []header
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(tr)
		out = append(out, header{h.Name, h.Mode, h.Typeflag, h.Uid, h.Gid, h.Uname, h.Gname, h.ModTime, h.Format, data})
	}
}

// TestPackIsReproducible: the same files give the same bytes, whatever
// their order, and the headers carry nothing of the machine.
func TestPackIsReproducible(t *testing.T) {
	es := testEntries(t)
	a, err := Pack(es, defaultEpoch)
	if err != nil {
		t.Fatal(err)
	}
	reversed := slices.Clone(es)
	slices.Reverse(reversed)
	b, err := Pack(reversed, defaultEpoch)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("two packs of the same files differ")
	}
	hs := readArchive(t, a)
	var names []string
	for _, h := range hs {
		names = append(names, h.name)
		if h.uid != 0 || h.gid != 0 || h.uname != "" || h.gname != "" || !h.mtime.Equal(defaultEpoch) {
			t.Errorf("%s: owner %d:%d %q:%q, time %v", h.name, h.uid, h.gid, h.uname, h.gname, h.mtime)
		}
	}
	if !slices.IsSorted(names) {
		t.Errorf("entries are not sorted: %v", names)
	}
	later, _ := Pack(es, defaultEpoch.Add(time.Hour))
	if bytes.Equal(a, later) {
		t.Error("the time is not part of the archive")
	}
}

// TestBundleLayout: the files, their modes and contents; the migrations
// are exactly those the binary embeds, without the kernel's.
func TestBundleLayout(t *testing.T) {
	a, err := Pack(testEntries(t), defaultEpoch)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]header{}
	for _, h := range readArchive(t, a) {
		got[h.name] = h
	}
	file := func(p string) []byte {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	buildinfo := []byte("module=mfgraph\nversion=0.1.0\ncommit=" + strings.Repeat("ab", 20) + "\ngo=go1.26.1\nsource_date_epoch=1794787200\n")
	want := map[string]struct {
		mode int64
		data []byte
	}{
		"bin/mfgraph-server":               {0o755, []byte("\x7fELF fake server")},
		"etc/module.env":                   {0o644, file(packaging + "/module.env")},
		"scripts/acceptance-m1.0a.sh":      {0o755, file(packaging + "/acceptance-m1.0a.sh")},
		"scripts/acceptance-m1.0a-diff.md": {0o644, file(packaging + "/acceptance-m1.0a-diff.md")},
		"docs/events.md":                   {0o644, file("docs/events.md")},
		"BUILDINFO":                        {0o644, buildinfo},
		"LICENSE":                          {0o644, file("LICENSE")},
		"NOTICE":                           {0o644, file("NOTICE")},
		"THIRD_PARTY_NOTICES.md":           {0o644, file("THIRD_PARTY_NOTICES.md")},
	}
	embedded, _ := fs.ReadDir(migrations.MySQL(), ".")
	for _, e := range embedded {
		b, _ := fs.ReadFile(migrations.MySQL(), e.Name())
		want["migrations/"+e.Name()] = struct {
			mode int64
			data []byte
		}{0o644, b}
		// The copy in the bundle is the file of the repository too.
		if !bytes.Equal(b, file("migrations/mysql/"+e.Name())) {
			t.Errorf("embedded %s differs from migrations/mysql", e.Name())
		}
	}
	for _, d := range []string{"bin/", "docs/", "etc/", "migrations/", "scripts/"} {
		if h, ok := got[d]; !ok || h.typ != tar.TypeDir || h.mode != 0o755 {
			t.Errorf("directory %s: %+v", d, h)
		}
	}
	for name, w := range want {
		h, ok := got[name]
		if !ok || h.typ != tar.TypeReg || h.mode != w.mode || !bytes.Equal(h.data, w.data) {
			t.Errorf("%s: missing or different (mode %o)", name, h.mode)
		}
	}
	if len(got) != len(want)+5+1 { // + directories + manifest
		t.Errorf("%d entries, want %d: %v", len(got), len(want)+6, got)
	}
	for name := range got {
		if strings.Contains(name, "kernel") || strings.Contains(name, "0000_bootstrap") {
			t.Errorf("kernel migration in the bundle: %s", name)
		}
	}
	// The manifest: the template with the version, as the host reads it.
	var m map[string]any
	if err := json.Unmarshal(got["manifest.json"].data, &m); err != nil {
		t.Fatal(err)
	}
	health, _ := m["health"].(map[string]any)
	runtime, _ := m["runtime"].(map[string]any)
	if m["id"] != "mfgraph" || m["version"] != "0.1.0" || m["protocol"] != float64(1) || health["path"] != "/healthz" ||
		health["ready"] != "/readyz" || health["timeout_seconds"] != float64(30) || m["namespace"] != nil || m["api_version"] != nil ||
		runtime["type"] != "binary" || m["sha256"] != "" || m["signature"] != "" {
		t.Errorf("manifest: %v", m)
	}
	args, _ := json.Marshal(runtime["args"])
	if string(args) != `["serve","--config","etc/module.env","--socket","{socket}"]` {
		t.Errorf("runtime.args %s", args)
	}
	mig, _ := json.Marshal(m["migrations"])
	if string(mig) != `{"command":["bin/mfgraph-server","--config","etc/module.env","migrate"],"dir":"migrations"}` {
		t.Errorf("migrations %s", mig)
	}
	caps, _ := json.Marshal(m["capabilities"])
	if string(caps) != `["versioned-workspace","snapshot","branch","diff","restore"]` {
		t.Errorf("capabilities %s (lock comes with locks)", caps)
	}
	for _, r := range m["routes"].([]any) {
		if !strings.HasPrefix(r.(string), "/") {
			t.Errorf("route %v is not absolute", r)
		}
	}
	if _, err := RenderManifest([]byte(`{"version":"x"}`), "0.1.0"); err == nil {
		t.Error("a template without {{version}} was accepted")
	}
	if _, err := RenderManifest([]byte(`{"version":"{{version}}",}`), "0.1.0"); err == nil {
		t.Error("a template that is not JSON was accepted")
	}
}

// TestExtract: a bundle unpacks to its files with their modes; entries
// that leave the directory are refused.
func TestExtract(t *testing.T) {
	es := testEntries(t)
	a, err := Pack(es, defaultEpoch)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := Extract(bytes.NewReader(a), dir); err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		p := filepath.Join(dir, filepath.FromSlash(e.Name))
		b, err := os.ReadFile(p)
		fi, serr := os.Stat(p)
		if err != nil || serr != nil || !bytes.Equal(b, e.Data) || int64(fi.Mode().Perm()) != e.Mode {
			t.Errorf("%s: %v %v mode %v", e.Name, err, serr, fi.Mode())
		}
	}
	if _, err := Pack([]Entry{{Name: "../x", Mode: 0o644}}, defaultEpoch); err == nil {
		t.Error("an entry leaving the directory was packed")
	}
	var buf bytes.Buffer
	zw, _ := zstd.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	_ = tw.WriteHeader(&tar.Header{Name: "../evil", Mode: 0o644, Typeflag: tar.TypeReg})
	_ = tw.Close()
	_ = zw.Close()
	if err := Extract(&buf, t.TempDir()); err == nil {
		t.Error("an entry leaving the directory was extracted")
	}
}

// TestPinnedToolchain: the bundles are built with the Go version that
// go.mod pins, which is the toolchain these tests run with when nothing
// overrides it.
func TestPinnedToolchain(t *testing.T) {
	pinned, err := pinnedToolchain(filepath.Join(root, "go.mod"))
	if err != nil || !regexp.MustCompile(`^go1\.[0-9]+\.[0-9]+$`).MatchString(pinned) {
		t.Fatalf("toolchain of go.mod: %q %v", pinned, err)
	}
	dir := t.TempDir()
	gomod := filepath.Join(dir, "go.mod")
	if err := os.WriteFile(gomod, []byte("module x\n\ngo 1.26.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := pinnedToolchain(gomod); err == nil {
		t.Fatal("a go.mod without toolchain was accepted")
	}
	if got := string(testInfo.Text()); !strings.Contains(got, "\ngo=go1.26.1\n") || strings.Contains(got, "{") {
		t.Fatalf("BUILDINFO: %q", got)
	}
}

// TestReleaseWorkflow: the release of a module version publishes the two
// bundles, sha256sums.txt and BUILDINFO.txt as release assets for a tag
// module-v<VERSION> only, with the pinned Go version and the
// reproducibility check, and with the one permission it needs.
func TestReleaseWorkflow(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release-module.yml"))
	if err != nil {
		t.Fatal(err)
	}
	w := string(b)
	for _, want := range []string{
		"on:\n  push:\n    tags:\n      - 'module-v*'\n",
		"permissions:\n  contents: write\n",
		`version="${tag#module-v}"`,
		"packaging/host-module/VERSION",
		`if [ "$version" != "$want" ]; then`,
		"go-version: ${{ steps.toolchain.outputs.version }}",
		`test "$(go env GOVERSION)" = "go${{ steps.toolchain.outputs.version }}"`,
		"- run: make bundle\n",
		"run: make bundle-check\n",
		`grep -qx "commit=$(git rev-parse HEAD)" dist/BUILDINFO.txt`,
		`gh release create "$GITHUB_REF_NAME" --verify-tag`,
		`"dist/mfgraph-$VERSION-linux-amd64.tar.zst" "dist/mfgraph-$VERSION-linux-arm64.tar.zst"`,
		"dist/sha256sums.txt dist/BUILDINFO.txt\n",
	} {
		if !strings.Contains(w, want) {
			t.Errorf("release-module.yml lacks %q", want)
		}
	}
	if strings.Count(w, "permissions:") != 1 || strings.Contains(w, "pull_request") || strings.Contains(w, "branches:") {
		t.Error("release-module.yml runs on more than tags or asks for more permissions")
	}
	ci, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	// Every CI job that builds a bundle uses the pinned Go version.
	if n := strings.Count(string(ci), "go-version: ${{ steps.toolchain.outputs.version }}"); n != 2 ||
		strings.Count(string(ci), "make bundle") < 2 {
		t.Errorf("ci.yml: %d jobs with the pinned Go version, want the bundle and e2e-mysql jobs", n)
	}
}
