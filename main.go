// Command role starts Claude Code in a role. See README.md.
package main

import (
	"os"

	"github.com/taktekhq/role/internal/role"
)

func main() {
	os.Exit(role.Main(os.Args[1:], os.Stdout, os.Stderr))
}
