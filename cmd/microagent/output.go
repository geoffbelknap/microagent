package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func writeJSON(stdout *os.File, value any) error {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func writeVersion(stdout *os.File) error {
	if outputStructured() {
		return writeJSON(stdout, map[string]any{
			"name":    "microagent",
			"version": version,
		})
	}
	fmt.Fprintf(stdout, "microagent %s\n", version)
	return nil
}
