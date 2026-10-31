// Command medgit is the append-only clinical object log.
package main

import (
	"os"

	"github.com/aomarnevarez/medgit/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
