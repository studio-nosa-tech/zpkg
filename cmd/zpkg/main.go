// Command zpkg is the ZIGNOS source-based package manager.
package main

import (
	"fmt"
	"os"

	"github.com/studio-nosa-tech/zpkg/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "zpkg:", err)
		os.Exit(1)
	}
}
