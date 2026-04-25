package main

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version 由 ldflags 注入; 未注入时回落到 go module 版本.
var (
	version   = "dev"
	commit    = ""
	buildDate = ""
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "打印 aswe 版本信息",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			v := version
			if v == "dev" {
				if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
					v = info.Main.Version
				}
			}
			fmt.Printf("aswe %s", v)
			if commit != "" {
				fmt.Printf(" (commit %s)", commit)
			}
			if buildDate != "" {
				fmt.Printf(" built %s", buildDate)
			}
			fmt.Println()
			return nil
		},
	}
}
