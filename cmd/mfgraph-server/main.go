// Command mfgraph-server runs the MFGraph HTTP service.
package main

import (
	"fmt"
	"os"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Printf("mfgraph-server %s\n", version)
		return
	}
	fmt.Fprintln(os.Stderr, "mfgraph-server: no server wiring in this build yet")
}
