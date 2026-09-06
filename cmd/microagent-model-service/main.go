// microagent-model-service runs the optional model service companion.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/geoffbelknap/microagent/internal/modelcompanion"
)

func main() {
	if err := modelcompanion.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
