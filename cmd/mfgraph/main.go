// Command mfgraph is the MFGraph command-line tool.
//
// M0 ships "version", "help" and the offline "debug" commands. The workspace
// subcommands are placeholders for the first core slice.
package main

import (
	"fmt"
	"io"
	"os"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// placeholders lists the client subcommands planned for the first core slice.
var placeholders = []struct{ name, summary string }{
	{"project", "create or show projects"},
	{"workspace", "create or show workspaces"},
	{"upload", "stage a file in a workspace"},
	{"commit", "commit staged files to a branch"},
	{"log", "show a branch timeline"},
	{"ls", "list a directory of a snapshot"},
	{"cat", "download a file from a snapshot"},
	{"diff", "compare two snapshots"},
	{"restore", "restore a snapshot (or a path) onto a branch"},
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "version", "-version", "--version":
		fmt.Fprintf(stdout, "mfgraph %s\n", version)
		return 0
	case "help", "-h", "-help", "--help":
		usage(stdout)
		return 0
	}
	for _, p := range placeholders {
		if args[0] == p.name {
			fmt.Fprintf(stderr, "mfgraph %s: not implemented in this build\n", p.name)
			return 1
		}
	}
	fmt.Fprintf(stderr, "mfgraph: unknown command %q\n\n", args[0])
	usage(stderr)
	return 2
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "Usage: mfgraph <command> [arguments]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintf(w, "  %-10s %s\n", "version", "print the version")
	fmt.Fprintf(w, "  %-10s %s\n", "help", "show this help")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Planned (not implemented in this build):")
	for _, p := range placeholders {
		fmt.Fprintf(w, "  %-10s %s\n", p.name, p.summary)
	}
}
