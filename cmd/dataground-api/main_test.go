package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestServeAPIRuntimeStopsBoundListenerWhenSecurityLifecycleFails(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind test listener: %v", err)
	}
	address := listener.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sentinel := errors.New("security lifecycle failure")
	server := &http.Server{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	err = serveAPIRuntime(ctx, cancel, logger, server, listener, func(context.Context) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("serve error = %v, want lifecycle failure", err)
	}
	connection, dialErr := net.DialTimeout("tcp", address, 100*time.Millisecond)
	if dialErr == nil {
		connection.Close()
		t.Fatal("listener remained reachable after lifecycle failure")
	}
}

func TestServeAPIRuntimeRejectsUnownedLifecycleCancellation(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind test listener: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := &http.Server{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	err = serveAPIRuntime(ctx, cancel, logger, server, listener, func(context.Context) error {
		return context.Canceled
	})
	if err == nil {
		t.Fatal("unowned lifecycle cancellation was treated as graceful shutdown")
	}
}
