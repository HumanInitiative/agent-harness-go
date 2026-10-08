// Command csrctl manages the CSR prospect index: importing seed companies,
// crawling them, and reviewing the results. Crawling is meant to be run by
// an external scheduler (cron, a Kubernetes CronJob), never by a timer
// inside the harness.
//
//	csrctl import seed.csv                 import or update seed companies
//	csrctl crawl [-discover-only] [-company NAME]
//	csrctl companies [-status new] [-limit 50]
//	csrctl show NAME                       routes, access, profile, evidence
//	csrctl route pin COMPANY_ID URL        record a CSR page by hand
//	csrctl route reject|restore PAGE_ID    correct the routing record
//	csrctl set-status COMPANY_ID new|verified|excluded
//
// Configuration comes from the same environment variables as the harness
// (see .env.example); output goes to stdout, logs to stderr.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
	_ "time/tzdata"

	"github.com/HumanInitiative/agent-harness-go/internal/adapters/outbound/genkitmodel"
	"github.com/HumanInitiative/agent-harness-go/internal/bootstrap"
	"github.com/HumanInitiative/agent-harness-go/internal/platform/config"
	"github.com/HumanInitiative/agent-harness-go/internal/platform/logger"
	"github.com/HumanInitiative/agent-harness-go/internal/websearch/csr"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "csrctl:", err)
		os.Exit(1)
	}
}

const usage = `usage: csrctl <command> [arguments]

commands:
  import FILE                      import seed companies from CSV or JSON
  crawl [-discover-only] [-company NAME]
                                   crawl due companies (or one company now)
  companies [-status S] [-limit N] list companies
  show NAME                        show a company's routes, profile and evidence
  route pin COMPANY_ID URL         record a CSR page by hand (never replaced automatically)
  route reject PAGE_ID             mark a route as not a CSR page
  route restore PAGE_ID            make a rejected or pinned route automatic again
  set-status COMPANY_ID STATUS     set review status: new, verified or excluded`

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	cfg, err := config.LoadCrawler()
	if err != nil {
		return err
	}
	log := logger.New(os.Stderr, cfg.LogLevel, cfg.LogFormat)

	store, err := bootstrap.OpenCSRStore(ctx, cfg.CSR)
	if err != nil {
		return err
	}
	defer store.Close()

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "import":
		return cmdImport(ctx, store, rest, out)
	case "crawl":
		return cmdCrawl(ctx, cfg, store, log, rest, out)
	case "companies":
		return cmdCompanies(ctx, store, rest, out)
	case "show":
		return cmdShow(ctx, cfg, store, rest, out)
	case "route":
		return cmdRoute(ctx, store, rest, out)
	case "set-status":
		return cmdSetStatus(ctx, store, rest, out)
	default:
		return fmt.Errorf("unknown command %q\n%s", cmd, usage)
	}
}

func cmdImport(ctx context.Context, store *csr.Store, args []string, out io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: csrctl import FILE (.csv or .json)")
	}
	f, err := os.Open(args[0])
	if err != nil {
		return err
	}
	defer f.Close()

	var rows []csr.SeedRow
	var problems []string
	switch strings.ToLower(filepath.Ext(args[0])) {
	case ".json":
		rows, err = csr.ReadSeedJSON(f)
	default:
		rows, problems, err = csr.ReadSeedCSV(f)
	}
	if err != nil {
		return err
	}
	report, err := csr.ImportSeed(ctx, store, rows)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "imported %d rows: %d new, %d updated, %d unchanged\n", len(rows), report.Inserted, report.Updated, report.Unchanged)
	for _, p := range append(problems, report.Problems...) {
		fmt.Fprintln(out, "  skipped/adjusted:", p)
	}
	return nil
}

func cmdCrawl(ctx context.Context, cfg config.CrawlerConfig, store *csr.Store, log *slog.Logger, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("crawl", flag.ContinueOnError)
	discoverOnly := fs.Bool("discover-only", false, "find and check routes without calling the LLM")
	company := fs.String("company", "", "crawl this company now, regardless of its schedule")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Automated crawling always honours robots.txt and goes slower than
	// interactive use, both per site and towards search engines.
	web, err := bootstrap.NewWebStack(cfg.Web, bootstrap.WebStackOptions{
		RespectRobots:       true,
		DomainRatePerSecond: 1 / cfg.DomainInterval.Seconds(),
		SearchInterval:      cfg.SearchInterval,
		MaxPDFBytes:         cfg.MaxPDFBytes,
		PDFTimeout:          cfg.PDFTimeout,
	}, log)
	if err != nil {
		return err
	}
	var extractor *csr.ProfileExtractor
	if !*discoverOnly {
		if cfg.GeminiAPIKey == "" {
			return errors.New("GEMINI_API_KEY is required for extraction (or use -discover-only)")
		}
		model, err := genkitmodel.New(ctx, genkitmodel.Config{Model: cfg.GenkitModel, APIKey: cfg.GeminiAPIKey}, log)
		if err != nil {
			return err
		}
		extractor = csr.NewProfileExtractor(model, log)
	}
	resolver := csr.NewResolver(store, web.Fetcher, web.Router, csr.ResolveOptions{}, log)
	crawler := csr.NewCrawler(store, resolver, web.Fetcher, extractor, csr.CrawlOptions{
		Workers: cfg.Workers, CompaniesPerRun: cfg.CompaniesPerRun, SkipExtraction: *discoverOnly,
	}, log)

	var report csr.CrawlReport
	if *company != "" {
		c, err := findOne(ctx, store, *company)
		if err != nil {
			return err
		}
		report, err = crawler.CrawlCompany(ctx, c)
		if err != nil {
			return err
		}
		report.Companies = 1
	} else if report, err = crawler.RunOnce(ctx); err != nil {
		return err
	}

	fmt.Fprintf(out, "companies %d | resolved %d (routes found %d) | pages fetched %d, changed %d | extracted %d, failed %d\n",
		report.Companies, report.Resolved, report.RoutesFound, report.PagesFetched, report.PagesChanged, report.Extracted, report.ExtractFailed)
	for _, e := range report.Errors {
		fmt.Fprintln(out, "  error:", e)
	}
	return nil
}

func cmdCompanies(ctx context.Context, store *csr.Store, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("companies", flag.ContinueOnError)
	status := fs.String("status", "", "filter by review status: new, verified, excluded")
	limit := fs.Int("limit", 50, "maximum rows")
	if err := fs.Parse(args); err != nil {
		return err
	}
	companies, err := store.ListCompanies(ctx, *status, *limit)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tDOMAIN\tDOMAIN STATUS\tACCESS\tROUTES\tCONFIDENCE\tSTATUS")
	for _, c := range companies {
		access := "-"
		if a, err := store.DomainAccess(ctx, c.Domain); err == nil {
			access = a.Status
		}
		pages, err := store.Pages(ctx, c.ID)
		if err != nil {
			return err
		}
		usable := 0
		for _, p := range pages {
			if p.State == csr.PageActive || p.State == csr.PagePinned {
				usable++
			}
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%d\t%.2f\t%s\n", c.ID, c.Name, dash(c.Domain), c.DomainStatus, access, usable, c.Confidence, c.Status)
	}
	return w.Flush()
}

func cmdShow(ctx context.Context, cfg config.CrawlerConfig, store *csr.Store, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: csrctl show NAME")
	}
	c, err := findOne(ctx, store, strings.Join(args, " "))
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s (id %d)\n  sector %s, region %s, source %s, review status %s\n", c.Name, c.ID, dash(c.Sector), dash(c.Region), c.Source, c.Status)
	fmt.Fprintf(out, "  domain %s (%s)", dash(c.Domain), c.DomainStatus)
	if a, err := store.DomainAccess(ctx, c.Domain); err == nil {
		fmt.Fprintf(out, ", access %s %s", a.Status, a.Detail)
	}
	fmt.Fprintf(out, "\n  last crawled %s, next crawl %s\n\nroutes:\n", fmtTime(c.LastCrawledAt), fmtTime(c.NextCrawlAt))

	pages, err := store.Pages(ctx, c.ID)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  PAGE\tSTATE\tKIND\tVIA\tSCORE\tHTTP\tURL")
	for _, p := range pages {
		fmt.Fprintf(w, "  %d\t%s\t%s\t%s\t%.1f\t%d\t%s\n", p.ID, p.State, p.Kind, p.DiscoveredVia, p.Score, p.HTTPStatus, p.URL)
	}
	if err := w.Flush(); err != nil {
		return err
	}

	profile, err := bootstrap.LoadInstitutionProfile(cfg.CSR)
	if err != nil {
		fmt.Fprintf(out, "\n(institution profile unavailable, fit not scored: %v)\n", err)
	}
	prospects, err := csr.CheckCompany(ctx, store, profile, c.Name)
	if err != nil || len(prospects) == 0 {
		return err
	}
	p := prospects[0]
	if p.Profile == nil {
		fmt.Fprintln(out, "\nno CSR profile extracted yet")
		return nil
	}
	fmt.Fprintf(out, "\nprofile (model confidence %.2f, extracted %s):\n", p.Profile.ModelConfidence, p.Profile.ExtractedAt.Format("2006-01-02"))
	printClaims := func(label string, claims []csr.Claim) {
		for _, cl := range claims {
			fmt.Fprintf(out, "  %-16s %s %v\n", label, cl.Value, cl.EvidenceIDs)
		}
	}
	printClaims("focus", p.Profile.FocusAreas)
	printClaims("region", p.Profile.Regions)
	printClaims("program type", p.Profile.ProgramTypes)
	printClaims("partner", p.Profile.KnownPartners)
	if p.Profile.ProposalChannel != nil {
		printClaims("proposal", []csr.Claim{*p.Profile.ProposalChannel})
	}
	if p.Profile.SeekingPartners != nil {
		printClaims("seeking partners", []csr.Claim{*p.Profile.SeekingPartners})
	}
	fmt.Fprintln(out, "\nevidence:")
	ids := make([]int64, 0, len(p.Evidence))
	for id := range p.Evidence {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		e := p.Evidence[id]
		fmt.Fprintf(out, "  [%d] %s\n      %q\n", id, e.URL, e.Excerpt)
	}
	fmt.Fprintf(out, "\nfit with institution: %d/100\n", p.Match.Score)
	for _, r := range p.Match.Reasons {
		fmt.Fprintln(out, "  -", r)
	}
	return nil
}

func cmdRoute(ctx context.Context, store *csr.Store, args []string, out io.Writer) error {
	if len(args) < 2 {
		return errors.New("usage: csrctl route pin COMPANY_ID URL | route reject PAGE_ID | route restore PAGE_ID")
	}
	switch args[0] {
	case "pin":
		if len(args) != 3 {
			return errors.New("usage: csrctl route pin COMPANY_ID URL")
		}
		companyID, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return fmt.Errorf("invalid company id %q", args[1])
		}
		if _, err := store.Company(ctx, companyID); err != nil {
			return err
		}
		score := csr.ScoreLink("", args[2])
		id, err := store.UpsertPage(ctx, csr.Page{CompanyID: companyID, URL: args[2], Kind: score.Kind, DiscoveredVia: csr.ViaManual, Score: score.Score})
		if err != nil {
			return err
		}
		if err := store.SetPageState(ctx, id, csr.PagePinned); err != nil {
			return err
		}
		fmt.Fprintf(out, "pinned page %d (%s) for company %d\n", id, score.Kind, companyID)
	case "reject", "restore":
		pageID, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return fmt.Errorf("invalid page id %q", args[1])
		}
		state := csr.PageRejected
		if args[0] == "restore" {
			state = csr.PageActive
		}
		if err := store.SetPageState(ctx, pageID, state); err != nil {
			return err
		}
		fmt.Fprintf(out, "page %d is now %s\n", pageID, state)
	default:
		return fmt.Errorf("unknown route action %q", args[0])
	}
	return nil
}

func cmdSetStatus(ctx context.Context, store *csr.Store, args []string, out io.Writer) error {
	if len(args) != 2 {
		return errors.New("usage: csrctl set-status COMPANY_ID new|verified|excluded")
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return fmt.Errorf("invalid company id %q", args[0])
	}
	if err := store.SetStatus(ctx, id, args[1]); err != nil {
		return err
	}
	fmt.Fprintf(out, "company %d status set to %s\n", id, args[1])
	return nil
}

// findOne resolves a company name to exactly one company, or explains why
// it cannot.
func findOne(ctx context.Context, store *csr.Store, name string) (csr.Company, error) {
	if c, err := store.CompanyByNormalizedName(ctx, csr.NormalizeName(name)); err == nil {
		return c, nil
	}
	matches, err := store.FindCompanies(ctx, name, 5)
	if err != nil {
		return csr.Company{}, err
	}
	switch len(matches) {
	case 0:
		return csr.Company{}, fmt.Errorf("no company matches %q", name)
	case 1:
		return matches[0], nil
	}
	var names []string
	for _, m := range matches {
		names = append(names, fmt.Sprintf("%s (id %d)", m.Name, m.ID))
	}
	return csr.Company{}, fmt.Errorf("%q is ambiguous: %s", name, strings.Join(names, ", "))
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}
