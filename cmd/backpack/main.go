// Command backpack reads photos of school papers with a local model and
// shows the week on one page at http://127.0.0.1:7313.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime/debug"
)

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("backpack", version())
		return
	}
	fmt.Fprintln(os.Stderr, "backpack: the server is not built yet")
	os.Exit(1)
}

// version reports the module version when installed with go install,
// and "dev" for a local build.
func version() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
