package main

import (
	"fmt"
	"os"

	"forgeworld/internal/app"
)

func main() {
	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
