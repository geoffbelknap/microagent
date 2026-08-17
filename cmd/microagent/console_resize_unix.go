//go:build linux || darwin

package main

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/geoffbelknap/microagent/pkg/workspace"
	"golang.org/x/term"
)

func startConsoleResize(file *os.File, conn net.Conn) (func(), error) {
	resize := func() (bool, error) {
		cols, rows, err := term.GetSize(int(file.Fd()))
		if err != nil {
			return false, err
		}
		return workspace.ResizeConsole(conn, rows, cols)
	}
	changes := make(chan os.Signal, 1)
	signal.Notify(changes, syscall.SIGWINCH)
	supported, err := resize()
	if err != nil {
		signal.Stop(changes)
		return nil, fmt.Errorf("set initial console size: %w", err)
	}
	if !supported {
		signal.Stop(changes)
		return func() {}, nil
	}

	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-changes:
				_, _ = resize()
			case <-done:
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			signal.Stop(changes)
			close(done)
		})
	}, nil
}
