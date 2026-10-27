// Command generate writes the command reference in docs/commands from the
// command catalog.
package main

import (
	"github.com/vincentsch/mailmd/internal/cli"
	"github.com/vincentsch/rungrad/docsgen"
	"log"
)

func main() {
	if err := docsgen.Write(cli.New(cli.Options{}), "docs/commands"); err != nil {
		log.Fatal(err)
	}
}
