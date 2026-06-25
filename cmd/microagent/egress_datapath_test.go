package main

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestExitWhenParentExitsCallsExitOnParentChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exited := make(chan struct{})
	go exitWhenParentExits(ctx, os.Getppid()+1000000, func() { close(exited) })
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("exitWhenParentExits did not call exit after parent mismatch")
	}
}
