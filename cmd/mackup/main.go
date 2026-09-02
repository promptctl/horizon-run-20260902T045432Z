// Command mackup keeps application settings in sync by keeping the one real
// copy of each config file in a folder that already syncs between machines.
//
// The observable surface -- command grammar, output, config filenames -- is the
// one specified in appspec/. "macklebox" is the project name; the command is
// mackup.
package main

import (
	"os"

	"github.com/promptctl/macklebox/internal/app"
	"github.com/promptctl/macklebox/internal/ui"
)

func main() {
	os.Exit(app.Main(os.Args[1:], &ui.IO{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}))
}
