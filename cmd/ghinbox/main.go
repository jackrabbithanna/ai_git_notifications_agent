// Command ghinbox is the headless companion to the GH Inbox desktop app.
// M0 only knows how to report the GitHub MCP server binary the app would use;
// sync/judge/analyze subcommands arrive with later milestones.
package main

import (
	"fmt"
	"os"

	"ghinbox/internal/mcpbin"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "mcp":
		if len(os.Args) >= 3 && os.Args[2] == "path" {
			info, err := mcpbin.Locate(mcpbin.Options{OverridePath: os.Getenv("GHINBOX_MCP_PATH")})
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			fmt.Printf("path:    %s\nsource:  %s\nversion: %s\n", info.Path, info.Source, orUnknown(info.Version))
			return
		}
		usage()
		os.Exit(2)
	case "version":
		v, ok := mcpbin.BundledVersion()
		fmt.Printf("ghinbox (M0 scaffold); bundled github-mcp-server: %s\n", map[bool]string{true: v, false: "none"}[ok])
	default:
		usage()
		os.Exit(2)
	}
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  ghinbox mcp path     locate the github-mcp-server binary (GHINBOX_MCP_PATH overrides)
  ghinbox version      print version info`)
}
