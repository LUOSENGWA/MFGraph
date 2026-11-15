package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/pathx"
	"github.com/LUOSENGWA/MFGraph/internal/tree"
)

func debugUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  mfgraph debug hash <file>...")
	fmt.Fprintln(w, "      print sha256:<hex> and the size in bytes of each file")
	fmt.Fprintln(w, "  mfgraph debug tree <directory> [--out <dir>]")
	fmt.Fprintln(w, "      build the manifest tree of a local directory (offline) and print every node;")
	fmt.Fprintln(w, "      --out writes each node's canonical bytes to <dir>/<hex>.json (uncompressed,")
	fmt.Fprintln(w, "      no trailing newline) so that `shasum -a 256` can check the hashes")
}

// unicodeTables reports the Unicode versions of the tables of this build.
// Only tests replace it, to simulate other toolchains.
var unicodeTables = pathx.TableVersions

// warnUnicodeTables prints a warning (and continues) when the Unicode tables
// of this build do not match the specification: debug tools are offline
// helpers, unlike the server, which refuses to start.
func warnUnicodeTables(stderr io.Writer) {
	std, xnorm, xcases := unicodeTables()
	if err := pathx.CheckUnicodeVersions(std, xnorm, xcases); err != nil {
		fmt.Fprintf(stderr, "mfgraph debug: warning: the Unicode tables of this build do not match the specification "+
			"(required V = %s; unicode %s, x/text norm %s, x/text cases %s); path normalization may differ:\n%v\n",
			pathx.UnicodeVersion, std, xnorm, xcases, err)
	}
}

func runDebug(args []string, stdout, stderr io.Writer) int {
	warnUnicodeTables(stderr)
	if len(args) == 0 {
		debugUsage(stderr)
		return 2
	}
	switch args[0] {
	case "hash":
		return debugHash(args[1:], stdout, stderr)
	case "tree":
		return debugTree(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		debugUsage(stdout)
		return 0
	}
	fmt.Fprintf(stderr, "mfgraph debug: unknown command %q\n", args[0])
	debugUsage(stderr)
	return 2
}

func debugHash(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		debugUsage(stderr)
		return 2
	}
	status := 0
	for _, name := range args {
		h, n, err := hashFile(name)
		if err != nil {
			fmt.Fprintf(stderr, "mfgraph debug hash: %v\n", err)
			status = 1
			continue
		}
		fmt.Fprintf(stdout, "%s  %d  %s\n", h, n, name)
	}
	return status
}

func hashFile(name string) (contenthash.Hash, int64, error) {
	f, err := os.Open(name)
	if err != nil {
		return contenthash.Hash{}, 0, err
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && !st.Mode().IsRegular() {
		return contenthash.Hash{}, 0, fmt.Errorf("%s: not a regular file", name)
	}
	return contenthash.Sum(f)
}

// parseInterspersed parses flags that may appear before or after positional
// arguments and returns the positional arguments.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func debugTree(args []string, stdout, stderr io.Writer) int {
	fset := flag.NewFlagSet("mfgraph debug tree", flag.ContinueOnError)
	fset.SetOutput(stderr)
	out := fset.String("out", "", "write each node's canonical bytes to `dir`/<hex>.json")
	pos, err := parseInterspersed(fset, args)
	if err != nil {
		return 2
	}
	if len(pos) != 1 {
		debugUsage(stderr)
		return 2
	}
	files, warnings, err := scanDir(pos[0])
	for _, w := range warnings {
		fmt.Fprintf(stderr, "warning: %s\n", w)
	}
	if err != nil {
		fmt.Fprintf(stderr, "mfgraph debug tree: %v\n", err)
		return 1
	}
	root, nodes, err := tree.Build(files)
	if err != nil {
		fmt.Fprintf(stderr, "mfgraph debug tree: %v\n", err)
		return 1
	}
	if *out != "" {
		if err := os.MkdirAll(*out, 0o755); err != nil {
			fmt.Fprintf(stderr, "mfgraph debug tree: %v\n", err)
			return 1
		}
	}
	i := 0
	err = tree.WalkNodes(context.Background(), tree.MapGetter(nodes), root, func(ni tree.NodeInfo) error {
		i++
		dir := ni.Dir
		if dir == "" {
			dir = "."
		}
		fmt.Fprintf(stdout, "node %d  dir=%q  kind=%s  hash=%s  bytes=%d\n", i, dir, ni.Node.Kind, ni.Hash, len(ni.Canonical))
		stdout.Write(ni.Canonical)
		fmt.Fprintln(stdout)
		if *out != "" {
			return os.WriteFile(filepath.Join(*out, ni.Hash.Hex()+".json"), ni.Canonical, 0o644)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(stderr, "mfgraph debug tree: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "files %d  distinct nodes %d\n", len(files), len(nodes))
	fmt.Fprintf(stdout, "root %s\n", root)
	return 0
}

// scanDir lists the regular files below root as tree input. Symlinks and
// other non-regular files are skipped with a warning; directories without
// files are reported because directories are implicit in the tree.
func scanDir(root string) ([]tree.FileEntry, []string, error) {
	st, err := os.Stat(root)
	if err != nil {
		return nil, nil, err
	}
	if !st.IsDir() {
		return nil, nil, fmt.Errorf("%s: not a directory", root)
	}
	var files []tree.FileEntry
	var warnings []string
	var dirs []string
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		switch t := d.Type(); {
		case t&fs.ModeSymlink != 0:
			warnings = append(warnings, fmt.Sprintf("skipping symlink %q (symlinks are not part of the data model)", rel))
		case d.IsDir():
			dirs = append(dirs, rel)
		case t.IsRegular():
			info, err := d.Info()
			if err != nil {
				return err
			}
			h, n, err := hashFile(p)
			if err != nil {
				return err
			}
			files = append(files, tree.FileEntry{Path: rel, Hash: h, Size: n, Mode: tree.ModeFromPerm(info.Mode().Perm())})
		default:
			warnings = append(warnings, fmt.Sprintf("skipping %q (not a regular file)", rel))
		}
		return nil
	})
	if err != nil {
		return nil, warnings, err
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		if !hasFileUnder(files, d) {
			warnings = append(warnings, fmt.Sprintf("directory %q has no files and is not represented (directories are implicit)", d))
		}
	}
	return files, warnings, nil
}

func hasFileUnder(files []tree.FileEntry, dir string) bool {
	prefix := dir + "/"
	for _, f := range files {
		if strings.HasPrefix(f.Path, prefix) {
			return true
		}
	}
	return false
}
