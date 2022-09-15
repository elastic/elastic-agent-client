// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package cmd

import (
	"flag"
	"fmt"
	"os"

	"github.com/elastic/elastic-agent-client/v7/dev-tools/v2tool/server"
	"github.com/spf13/cobra"
)

// NewCommand returns a new command structure
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:  "v2tool -d -c ./input.yml [CLIENT_EXEC]",
		Args: cobra.ExactArgs(1),
	}
	cmd.PersistentFlags().AddGoFlag(flag.CommandLine.Lookup("c"))
	cmd.PersistentFlags().AddGoFlag(flag.CommandLine.Lookup("d"))

	run := runCmd()
	cmd.Run = run.Run

	return cmd
}

func runCmd() *cobra.Command {
	return &cobra.Command{
		Use: "run",
		Run: func(_ *cobra.Command, args []string) {
			if err := server.Run(args[0]); err != nil { // actual entrypoint for the v2tool
				fmt.Fprintf(os.Stderr, "Error: %v\n\n", err)
				os.Exit(1)
			}
		},
	}
}
