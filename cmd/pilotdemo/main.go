// Command pilotdemo runs both pilots in one process, with no outside
// dependency: the invented donation API, a CSR index filled with invented
// companies, and a scripted model that writes "[DEMO]" text. It is for
// learning how the pilots work, step by step (docs/pilots/praktik.md); it
// never contacts Gemini and must never be used for real work.
//
//	make demo                              # then follow docs/pilots/praktik.md
//	go run ./cmd/pilotdemo -quirk fake-email
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	_ "github.com/HumanInitiative/agent-harness-go/api/swagger" // registers the generated OpenAPI spec
	"github.com/HumanInitiative/agent-harness-go/internal/adapters/inbound/httpapi"
	"github.com/HumanInitiative/agent-harness-go/internal/adapters/outbound/csrprospects"
	"github.com/HumanInitiative/agent-harness-go/internal/adapters/outbound/hiprograms"
	"github.com/HumanInitiative/agent-harness-go/internal/adapters/outbound/memory"
	"github.com/HumanInitiative/agent-harness-go/internal/adapters/outbound/reportapi"
	"github.com/HumanInitiative/agent-harness-go/internal/adapters/outbound/reportdocx"
	"github.com/HumanInitiative/agent-harness-go/internal/application/agent"
	"github.com/HumanInitiative/agent-harness-go/internal/application/prospecting"
	"github.com/HumanInitiative/agent-harness-go/internal/application/reporting"
	"github.com/HumanInitiative/agent-harness-go/internal/mockapi"
	"github.com/HumanInitiative/agent-harness-go/internal/platform/logger"
	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
	"github.com/HumanInitiative/agent-harness-go/internal/websearch/csr"
)

// demoAPIKey is the X-API-Key of the demo harness. It is public on purpose:
// the demo holds nothing but invented data.
const demoAPIKey = "demo-key-only-for-learning-0000000000"

type options struct {
	addr, mockAddr, dbPath, programsFile, quirk string
	log                                         *slog.Logger
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "pilotdemo:", err)
		os.Exit(1)
	}
}

func run() error {
	var o options
	flag.StringVar(&o.addr, "addr", "127.0.0.1:8080", "address of the harness")
	flag.StringVar(&o.mockAddr, "mock-addr", "127.0.0.1:8090", "address of the invented donation API")
	flag.StringVar(&o.dbPath, "db", "data/demo/csr.db", "CSR index file; recreated at every start")
	flag.StringVar(&o.programsFile, "programs", "config/hi-programs.example.yaml", "catalog of Human Initiative programs")
	flag.StringVar(&o.quirk, "quirk", "", "make the fake model misbehave once: "+strings.Join(quirks, ", "))
	flag.Parse()
	o.log = logger.New(os.Stdout, slog.LevelInfo, "text")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	d, err := build(ctx, o)
	if err != nil {
		return err
	}
	defer d.close()

	fmt.Printf(`
==============================  DEMO  ==============================
 Model PALSU, data karangan. Tidak menghubungi Gemini. Jangan dipakai
 untuk pekerjaan nyata.

 Harness      http://%s     (Swagger: /swagger/index.html)
 Halaman      http://%s/reports
 API donasi   %s            (tiruan)
 X-API-Key    %s
 Indeks CSR   %s   (%d perusahaan karangan)
 Katalog      %s
 Quirk model  %s
 Langkah      docs/pilots/praktik.md
=====================================================================

`, o.addr, o.addr, d.mockURL, demoAPIKey, o.dbPath, len(demoCompanies), o.programsFile, quirkLabel(o.quirk))

	errc := make(chan error, 1)
	go func() { errc <- d.server.Serve(d.listener) }()
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return d.server.Shutdown(shutdown)
	}
	return nil
}

func quirkLabel(q string) string {
	if q == "" {
		return "(tidak ada)"
	}
	return q
}

// demo is the running set of components.
type demo struct {
	server   *http.Server
	listener net.Listener
	mockURL  string
	closers  []func()
}

func (d *demo) close() {
	for i := len(d.closers) - 1; i >= 0; i-- {
		d.closers[i]()
	}
}

// build assembles the demo without starting the harness's own server: the
// invented API is already listening, the index is seeded, and the handler is
// ready on d.server and d.listener.
func build(ctx context.Context, o options) (*demo, error) {
	valid := o.quirk == ""
	for _, q := range quirks {
		valid = valid || o.quirk == q
	}
	if !valid {
		return nil, fmt.Errorf("unknown -quirk %q; use one of: %s", o.quirk, strings.Join(quirks, ", "))
	}
	d := &demo{}
	ok := false
	defer func() {
		if !ok {
			d.close()
		}
	}()

	// 1. The invented donation API.
	mock, err := mockapi.New(mockapi.Options{})
	if err != nil {
		return nil, err
	}
	mockListener, err := net.Listen("tcp", o.mockAddr)
	if err != nil {
		return nil, fmt.Errorf("the donation API cannot listen on %s: %w", o.mockAddr, err)
	}
	mockServer := &http.Server{Handler: mock, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = mockServer.Serve(mockListener) }()
	d.closers = append(d.closers, func() { _ = mockServer.Close() })
	d.mockURL = "http://" + mockListener.Addr().String()

	// 2. The CSR index, recreated so that every start is the same.
	if err := os.MkdirAll(filepath.Dir(o.dbPath), 0o750); err != nil {
		return nil, err
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(o.dbPath + suffix)
	}
	clock := &offsetClock{}
	store, err := csr.OpenStore(ctx, o.dbPath, clock.now)
	if err != nil {
		return nil, err
	}
	d.closers = append(d.closers, func() { _ = store.Close() })
	if err := seedDemoIndex(ctx, store, clock); err != nil {
		return nil, err
	}

	// 3. The pilots, over the fake model.
	model := demoModel{quirk: o.quirk}
	renderers := []outbound.DraftRenderer{reportdocx.New()}

	client, err := reportapi.New(reportapi.Config{BaseURL: d.mockURL, Timeout: 5 * time.Second}, nil, o.log)
	if err != nil {
		return nil, err
	}
	reportService, err := reporting.NewService(client, client, model, renderers, reporting.DefaultConfig(), o.log)
	if err != nil {
		return nil, err
	}
	reportService.WithPrograms(client)

	pc := prospecting.DefaultConfig()
	// Example prices, only so that the trace shows a cost.
	pc.InputRupiahPerMillion, pc.OutputRupiahPerMillion = 3000, 12000
	outreachService, err := prospecting.NewService(csrprospects.New(store, 0.6, csr.IndexOptions{}), model, renderers, pc, o.log)
	if err != nil {
		return nil, err
	}
	catalog, err := hiprograms.Load(o.programsFile)
	if err != nil {
		return nil, fmt.Errorf("%w (run from the repository root, or pass -programs)", err)
	}
	outreachService.WithCatalog(catalog)

	conversations, err := memory.New(memory.Options{MaxConversations: 100, MaxMessagesPerConversation: 50, TTL: time.Hour})
	if err != nil {
		return nil, err
	}
	chatService, err := agent.NewService(model, conversations, memory.NewTurnGuard(), nil,
		agent.Config{MaxToolTurns: 2, HistoryWindow: 10, MaxMessageChars: 2000}, o.log)
	if err != nil {
		return nil, err
	}

	// 4. The harness's own HTTP layer, exactly as cmd/harness mounts it.
	handler, err := httpapi.NewRouter(httpapi.NewChatHandler(chatService, o.log), o.log, httpapi.RouterConfig{
		RequestTimeout:     60 * time.Second,
		MaxRequestBytes:    64 << 10,
		SwaggerEnabled:     true,
		APIKeys:            []string{demoAPIKey},
		RateLimitPerMinute: 600,
		RateLimitBurst:     100,
		Reports:            httpapi.NewReportHandler(reportService, o.log),
		Outreach:           httpapi.NewOutreachHandler(outreachService, o.log),
	})
	if err != nil {
		return nil, err
	}
	d.listener, err = net.Listen("tcp", o.addr)
	if err != nil {
		return nil, fmt.Errorf("the harness cannot listen on %s: %w", o.addr, err)
	}
	d.closers = append(d.closers, func() { _ = d.listener.Close() })
	d.server = &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 70 * time.Second}
	ok = true
	return d, nil
}
