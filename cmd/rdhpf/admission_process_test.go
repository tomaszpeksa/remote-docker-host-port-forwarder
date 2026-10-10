package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/config"
)

func TestRunDuplicateProcess(t *testing.T) {
	if os.Getenv("S06_DUPLICATE_PROCESS") != "1" {
		return
	}
	fmt.Println(run(context.Background(), &config.Config{Host: os.Getenv("S06_DUPLICATE_TARGET")}, slog.New(slog.NewTextHandler(io.Discard, nil))))
}
