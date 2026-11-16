// Command mfgraph is the MFGraph command-line tool: a thin client of the
// REST API, and offline tools for the content format.
//
// The client commands only call the API; every rule is the server's. They
// connect to MFGRAPH_URL (default http://127.0.0.1:8094; unix:<path> for a
// Unix domain socket) with the bearer token MFGRAPH_TOKEN and the API
// prefix MFGRAPH_API_PREFIX (default /api/v1); --url, --token and --prefix
// override them. --json prints the response bodies as JSON. An error
// response of the server is printed as received, and the exit status is 1
// (2 for a mistake in the command line).
package main

import (
	"context"
	"fmt"
	"io"
	"os"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// getenv is os.Getenv; tests replace it.
var getenv = os.Getenv

// clientCommands are the API client commands.
var clientCommands = []struct{ name, usage string }{
	{"project", "project create <name>                       create a project; prints its ID"},
	{"workspace", "workspace create <project_id> [--name <n>]  create a workspace; prints its ID and main branch"},
	{"upload", "upload <workspace_id> <file>...             stage files; prints hash, size, status, file"},
	{"commit", "commit <workspace_id> [--branch <id>] [-m <msg>] <path>=<file>... [--delete <path>]...\n" +
		"                                              upload the files and commit them; prints the snapshot ID"},
	{"log", "log <workspace_id> [--branch <id>]          list the snapshots, newest first"},
	{"ls", "ls <snapshot_id> [path]                     list a directory of a snapshot"},
	{"cat", "cat <snapshot_id> <path>                    write a file of a snapshot to stdout"},
	{"diff", "diff <snapshot_id> [--base <snapshot_id>]   list the changed files"},
	{"restore", "restore <snapshot_id> --branch <id> [--path <p>] [-m <msg>]\n" +
		"                                              restore a snapshot (or one path) onto a branch"},
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
	case "debug":
		return runDebug(args[1:], stdout, stderr)
	}
	for _, c := range clientCommands {
		if args[0] == c.name {
			return runClient(context.Background(), c.name, args[1:], getenv, stdout, stderr)
		}
	}
	fmt.Fprintf(stderr, "mfgraph: unknown command %q\n\n", args[0])
	usage(stderr)
	return 2
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "Usage: mfgraph <command> [arguments]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Client commands (MFGRAPH_URL, MFGRAPH_TOKEN, MFGRAPH_API_PREFIX; flags --url, --token, --prefix, --json):")
	for _, c := range clientCommands {
		fmt.Fprintf(w, "  %s\n", c.usage)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Other commands:")
	fmt.Fprintf(w, "  %-10s %s\n", "version", "print the version")
	fmt.Fprintf(w, "  %-10s %s\n", "help", "show this help")
	fmt.Fprintf(w, "  %-10s %s\n", "debug", "offline format tools: debug hash <file>, debug tree <dir> [--out <dir>]")
}
