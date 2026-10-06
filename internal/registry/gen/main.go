// Command gen writes internal/names/names.go from telemetry/registry, ynr's Weaver registry.
// Run it with `go generate ./...`.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/eyelock/ynr/internal/registry"
)

func main() {
	dir := flag.String("registry", "../../telemetry/registry", "the registry folder")
	out := flag.String("out", "names.go", "the Go file to write")
	flag.Parse()
	r, err := registry.Load(os.DirFS(*dir), "")
	if err == nil {
		var src []byte
		if src, err = registry.Generate(r, "names", "ynr."); err == nil {
			err = os.WriteFile(*out, src, 0o644)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}
