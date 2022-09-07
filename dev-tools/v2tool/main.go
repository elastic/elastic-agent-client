package main

import (
	"fmt"
	"os"

	"github.com/elastic/elastic-agent-client/v7/dev-tools/v2tool/cmd"
)

func main() {
	command := cmd.NewCommand()
	if err := command.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

}
