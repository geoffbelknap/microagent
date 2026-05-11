//go:build !darwin && !linux

package main

import "os"

func makeRawTerminal(file *os.File) (func(), error) {
	return func() {}, nil
}
