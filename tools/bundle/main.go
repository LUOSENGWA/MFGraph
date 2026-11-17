// Command bundle builds the bundles of the MFGraph module from
// packaging/host-module: for every platform a reproducible tar.zst with
//
//	bin/mfgraph-server                  the server (CGO_ENABLED=0, -trimpath, version from -version)
//	manifest.json                       rendered from manifest.json.tmpl
//	etc/module.env                      the static settings
//	migrations/*.sql                    the module's migrations, for audit (the binary embeds and runs them)
//	scripts/acceptance-m1.0a.sh         the acceptance script
//	scripts/acceptance-m1.0a-diff.md    the difference table, filled in beforehand
//	docs/events.md                      the event vocabulary
//	BUILDINFO                           the version, the commit and the Go version of the build
//	LICENSE, NOTICE, THIRD_PARTY_NOTICES.md
//
// and dist/sha256sums.txt and dist/BUILDINFO.txt (the same as BUILDINFO).
// Entries are sorted, have the time of SOURCE_DATE_EPOCH (default
// 2026-11-16T00:00:00Z) and owner 0:0, and zstd runs single-threaded, so
// two builds of the same commit produce the same bytes. They also need the
// same Go version: the build refuses any other than the toolchain that
// go.mod pins (the go command switches to it by itself unless GOTOOLCHAIN
// says otherwise). Compression uses github.com/klauspost/compress, not a
// zstd program.
//
//	go run ./tools/bundle [-version V] [-commit SHA] [-out dist] [-platforms linux/amd64,linux/arm64] [-verify]
//	go run ./tools/bundle extract FILE DIR
//
// The commit is that of HEAD (git rev-parse HEAD), with "-dirty" when a
// tracked file differs from it; -commit names it for a tree without git.
// -verify builds again, from an empty Go build cache, and checks that every
// archive equals the one in -out and its line in sha256sums.txt, and that
// BUILDINFO.txt is the same.
//
// Run it from the repository root. Nothing here names the host platform:
// that is confined to packaging/host-module.
package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/LUOSENGWA/MFGraph/migrations"
)

// packaging is the directory of the module's packaging files.
const packaging = "packaging/host-module"

// defaultEpoch is the time of every entry without SOURCE_DATE_EPOCH.
var defaultEpoch = time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)

var semverRe = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "extract" {
		if len(os.Args) != 4 {
			fail(errors.New("usage: bundle extract FILE DIR"))
		}
		f, err := os.Open(os.Args[2])
		if err != nil {
			fail(err)
		}
		defer f.Close()
		if err := Extract(f, os.Args[3]); err != nil {
			fail(err)
		}
		return
	}
	version := flag.String("version", "", "module version (default: "+packaging+"/VERSION)")
	commit := flag.String("commit", "", "the commit of the build (default: git rev-parse HEAD, -dirty with changed tracked files)")
	out := flag.String("out", "dist", "output directory")
	platforms := flag.String("platforms", "linux/amd64,linux/arm64", "comma-separated GOOS/GOARCH list")
	verify := flag.Bool("verify", false, "build again from an empty Go build cache and compare with the bundles in -out")
	flag.Parse()
	if *version == "" {
		b, err := os.ReadFile(filepath.Join(packaging, "VERSION"))
		if err != nil {
			fail(err)
		}
		*version = strings.TrimSpace(string(b))
	}
	if !semverRe.MatchString(*version) {
		fail(fmt.Errorf("version %q is not semver", *version))
	}
	epoch, err := sourceDateEpoch()
	if err != nil {
		fail(err)
	}
	info, err := buildInfo(*version, *commit, epoch)
	if err != nil {
		fail(err)
	}
	if *verify {
		if err := verifyBundles(info, *platforms, *out); err != nil {
			fail(err)
		}
		return
	}
	archives, err := buildAll(info, *platforms, "")
	if err != nil {
		fail(err)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fail(err)
	}
	var sums []string
	for _, a := range archives {
		if err := os.WriteFile(filepath.Join(*out, a.name), a.data, 0o644); err != nil {
			fail(err)
		}
		sums = append(sums, a.sum+"  "+a.name)
		fmt.Printf("%s  %s (%d bytes)\n", a.sum, a.name, len(a.data))
	}
	if err := os.WriteFile(filepath.Join(*out, "sha256sums.txt"), []byte(strings.Join(sums, "\n")+"\n"), 0o644); err != nil {
		fail(err)
	}
	if err := os.WriteFile(filepath.Join(*out, "BUILDINFO.txt"), info.Text(), 0o644); err != nil {
		fail(err)
	}
	fmt.Print(string(info.Text()))
}

// BuildInfo is the content of BUILDINFO (in each bundle) and of
// dist/BUILDINFO.txt: what a rebuild needs to give the same bytes.
type BuildInfo struct {
	Version   string    // the module version
	Commit    string    // the commit (40 hex digits, "-dirty" with local changes)
	GoVersion string    // the Go toolchain, for example go1.26.1
	Epoch     time.Time // SOURCE_DATE_EPOCH
}

// Text is the file: one KEY=VALUE per line.
func (b BuildInfo) Text() []byte {
	return []byte(fmt.Sprintf("module=mfgraph\nversion=%s\ncommit=%s\ngo=%s\nsource_date_epoch=%d\n", b.Version, b.Commit,
		b.GoVersion, b.Epoch.Unix()))
}

var commitRe = regexp.MustCompile(`^[0-9a-f]{40}(-dirty)?$`)

// buildInfo collects the build information: the commit (from git unless
// given) and the Go version, which must be the toolchain go.mod pins.
func buildInfo(version, commit string, epoch time.Time) (BuildInfo, error) {
	if commit == "" {
		head, err := exec.Command("git", "rev-parse", "HEAD").Output()
		if err != nil {
			return BuildInfo{}, fmt.Errorf("cannot read the commit with git (%v); pass -commit", err)
		}
		commit = strings.TrimSpace(string(head))
		changed, err := exec.Command("git", "status", "--porcelain", "--untracked-files=no").Output()
		if err != nil {
			return BuildInfo{}, fmt.Errorf("git status: %v", err)
		}
		if len(bytes.TrimSpace(changed)) > 0 {
			commit += "-dirty"
		}
	}
	if !commitRe.MatchString(commit) {
		return BuildInfo{}, fmt.Errorf("commit %q is not 40 hex digits", commit)
	}
	goVersion, err := exec.Command("go", "env", "GOVERSION").Output()
	if err != nil {
		return BuildInfo{}, fmt.Errorf("go env GOVERSION: %v", err)
	}
	pinned, err := pinnedToolchain("go.mod")
	if err != nil {
		return BuildInfo{}, err
	}
	have := strings.TrimSpace(string(goVersion))
	if have != pinned {
		return BuildInfo{}, fmt.Errorf("the build would use %s, but go.mod pins %s (toolchain): bundles are reproducible only "+
			"with that version; unset GOTOOLCHAIN or install it", have, pinned)
	}
	return BuildInfo{Version: version, Commit: commit, GoVersion: have, Epoch: epoch}, nil
}

// pinnedToolchain returns the toolchain line of a go.mod, for example
// go1.26.1.
func pinnedToolchain(gomod string) (string, error) {
	b, err := os.ReadFile(gomod)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "toolchain "); ok && strings.HasPrefix(v, "go") {
			return strings.TrimSpace(v), nil
		}
	}
	return "", errors.New("go.mod has no toolchain line: the bundles need a pinned Go version")
}

// archive is one built bundle.
type archive struct {
	name, sum string
	data      []byte
}

// buildAll builds the bundle of every platform, sorted by file name. A
// non-empty gocache is the Go build cache to build with.
func buildAll(info BuildInfo, platforms string, gocache string) ([]archive, error) {
	var out []archive
	for _, p := range strings.Split(platforms, ",") {
		goos, goarch, ok := strings.Cut(strings.TrimSpace(p), "/")
		if !ok {
			return nil, fmt.Errorf("platform %q is not GOOS/GOARCH", p)
		}
		bin, err := buildServer(info.Version, goos, goarch, gocache)
		if err != nil {
			return nil, err
		}
		entries, err := Entries(".", info, bin)
		if err != nil {
			return nil, err
		}
		data, err := Pack(entries, info.Epoch)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		out = append(out, archive{name: fmt.Sprintf("mfgraph-%s-%s-%s.tar.zst", info.Version, goos, goarch),
			sum: hex.EncodeToString(sum[:]), data: data})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

// verifyBundles builds the bundles again from an empty Go build cache (in
// a temporary directory it removes afterwards) and compares them with the
// files in dir, their lines in dir/sha256sums.txt and dir/BUILDINFO.txt.
func verifyBundles(info BuildInfo, platforms, dir string) error {
	have, err := os.ReadFile(filepath.Join(dir, "BUILDINFO.txt"))
	if err != nil {
		return err
	}
	if !bytes.Equal(have, info.Text()) {
		return fmt.Errorf("%s/BUILDINFO.txt is of another build:\n%s\nthis build:\n%s", dir, have, info.Text())
	}
	tmp, err := os.MkdirTemp("", "mfgraph-bundle-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	again, err := buildAll(info, platforms, filepath.Join(tmp, "gocache"))
	if err != nil {
		return err
	}
	sums, err := os.ReadFile(filepath.Join(dir, "sha256sums.txt"))
	if err != nil {
		return err
	}
	for _, a := range again {
		have, err := os.ReadFile(filepath.Join(dir, a.name))
		if err != nil {
			return err
		}
		if !bytes.Equal(have, a.data) {
			return fmt.Errorf("%s: the rebuilt archive differs (%s)", a.name, a.sum)
		}
		if !strings.Contains(string(sums), a.sum+"  "+a.name+"\n") {
			return fmt.Errorf("%s: sha256sums.txt lacks %s", a.name, a.sum)
		}
		fmt.Printf("reproducible: %s  %s\n", a.sum, a.name)
	}
	return nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "bundle:", err)
	os.Exit(1)
}

func sourceDateEpoch() (time.Time, error) {
	v := os.Getenv("SOURCE_DATE_EPOCH")
	if v == "" {
		return defaultEpoch, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return time.Time{}, fmt.Errorf("SOURCE_DATE_EPOCH %q is not a number of seconds", v)
	}
	return time.Unix(n, 0).UTC(), nil
}

// buildServer cross-compiles bin/mfgraph-server: no cgo, no paths or VCS
// data or build ID in the binary, fixed instruction set levels; with a
// non-empty gocache in that build cache.
func buildServer(version, goos, goarch, gocache string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "mfgraph-bundle-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "mfgraph-server")
	cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false",
		"-ldflags", "-s -w -buildid= -X main.version="+version, "-o", out, "./cmd/mfgraph-server")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch, "GOAMD64=v1", "GOARM64=v8.0", "GOFLAGS=")
	if gocache != "" {
		cmd.Env = append(cmd.Env, "GOCACHE="+gocache)
	}
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("build %s/%s: %w", goos, goarch, err)
	}
	return os.ReadFile(out)
}

// Entry is one file of a bundle.
type Entry struct {
	Name string // slash-separated path inside the bundle
	Mode int64
	Data []byte
}

// Entries returns the files of a bundle: the server binary bin, BUILDINFO
// and the files of the repository at root.
func Entries(root string, info BuildInfo, bin []byte) ([]Entry, error) {
	read := func(p string) ([]byte, error) { return os.ReadFile(filepath.Join(root, filepath.FromSlash(p))) }
	tmpl, err := read(packaging + "/manifest.json.tmpl")
	if err != nil {
		return nil, err
	}
	manifest, err := RenderManifest(tmpl, info.Version)
	if err != nil {
		return nil, err
	}
	entries := []Entry{{Name: "bin/mfgraph-server", Mode: 0o755, Data: bin}, {Name: "manifest.json", Mode: 0o644, Data: manifest},
		{Name: "BUILDINFO", Mode: 0o644, Data: info.Text()}}
	for _, f := range []struct {
		name, src string
		mode      int64
	}{
		{"etc/module.env", packaging + "/module.env", 0o644},
		{"scripts/acceptance-m1.0a.sh", packaging + "/acceptance-m1.0a.sh", 0o755},
		{"scripts/acceptance-m1.0a-diff.md", packaging + "/acceptance-m1.0a-diff.md", 0o644},
		{"docs/events.md", "docs/events.md", 0o644},
		{"LICENSE", "LICENSE", 0o644},
		{"NOTICE", "NOTICE", 0o644},
		{"THIRD_PARTY_NOTICES.md", "THIRD_PARTY_NOTICES.md", 0o644},
	} {
		b, err := read(f.src)
		if err != nil {
			return nil, err
		}
		entries = append(entries, Entry{Name: f.name, Mode: f.mode, Data: b})
	}
	// The migrations the binary embeds (never the kernel's).
	err = fs.WalkDir(migrations.MySQL(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".sql") {
			return err
		}
		b, err := fs.ReadFile(migrations.MySQL(), p)
		if err == nil {
			entries = append(entries, Entry{Name: "migrations/" + p, Mode: 0o644, Data: b})
		}
		return err
	})
	return entries, err
}

// RenderManifest fills the version into the manifest template and checks
// that the result is a JSON object.
func RenderManifest(tmpl []byte, version string) ([]byte, error) {
	if !bytes.Contains(tmpl, []byte("{{version}}")) {
		return nil, errors.New("manifest template has no {{version}}")
	}
	out := bytes.ReplaceAll(tmpl, []byte("{{version}}"), []byte(version))
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		return nil, fmt.Errorf("rendered manifest: %w", err)
	}
	if m["version"] != version {
		return nil, errors.New("rendered manifest: version not set")
	}
	return out, nil
}

// Pack writes entries as a tar.zst: directories first created implicitly
// by their files' paths, everything sorted by name, every header with the
// time epoch, owner and group 0 and no user or group names (USTAR), zstd
// with one goroutine.
func Pack(entries []Entry, epoch time.Time) ([]byte, error) {
	files := append([]Entry(nil), entries...)
	dirs := map[string]bool{}
	for _, e := range files {
		if strings.HasPrefix(e.Name, "/") || strings.Contains("/"+e.Name+"/", "/../") || e.Name == "" {
			return nil, fmt.Errorf("bad entry name %q", e.Name)
		}
		for d := path.Dir(e.Name); d != "."; d = path.Dir(d) {
			dirs[d] = true
		}
	}
	for d := range dirs {
		files = append(files, Entry{Name: d + "/", Mode: 0o755})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	for i := 1; i < len(files); i++ {
		if files[i].Name == files[i-1].Name {
			return nil, fmt.Errorf("duplicate entry %q", files[i].Name)
		}
	}
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf, zstd.WithEncoderLevel(zstd.SpeedBetterCompression), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	tw := tar.NewWriter(zw)
	for _, e := range files {
		h := &tar.Header{Name: e.Name, Mode: e.Mode, ModTime: epoch, Format: tar.FormatUSTAR, Typeflag: tar.TypeReg,
			Size: int64(len(e.Data))}
		if strings.HasSuffix(e.Name, "/") {
			h.Typeflag, h.Size = tar.TypeDir, 0
		}
		if err := tw.WriteHeader(h); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name, err)
		}
		if _, err := tw.Write(e.Data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Extract unpacks a tar.zst bundle into dir (which it creates), keeping the
// file modes; paths that leave dir are refused.
func Extract(r io.Reader, dir string) error {
	zr, err := zstd.NewReader(r)
	if err != nil {
		return err
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(h.Name)
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("entry %q leaves the directory", h.Name)
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fs.FileMode(h.Mode)&0o777)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("entry %q has type %c", h.Name, h.Typeflag)
		}
	}
}
