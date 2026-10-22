// Command mockdonationapi serves invented donation data and activity
// documents over HTTP, standing in for Human Initiative's donation API so
// the report feature can be tried end to end. It is for development and
// demos, never production.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/mockapi"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mockdonationapi:", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", "127.0.0.1:8090", "listen address")
	key := flag.String("key", os.Getenv("MOCKAPI_KEY"), "required X-API-Key (default $MOCKAPI_KEY; empty means no key)")
	flag.Parse()

	srv, err := mockapi.New(mockapi.Options{APIKey: *key})
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           srv,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- httpServer.ListenAndServe() }()
	log.Info("mock donation API listening (invented data)", "addr", *addr, "key_required", *key != "")

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdown)
	}
	return nil
}
