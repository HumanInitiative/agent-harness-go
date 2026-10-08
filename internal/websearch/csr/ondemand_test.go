package csr

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/websearch"
)

func newOnDemandFixture(t *testing.T, opts OnDemandOptions) (crawlFixture, *OnDemand) {
	t.Helper()
	f := newCrawlFixture(t, CrawlOptions{}, goodExtraction)
	f.web.search[`"Contoh Energi" situs resmi`] = []websearch.Result{
		{Title: "PT Contoh Energi Tbk - Situs Resmi", URL: "https://contohenergi.co.id/"},
	}
	home := homepage(link("TJSL", "https://contohenergi.co.id/tjsl"))
	home.Title = "PT Contoh Energi Tbk"
	f.web.pages["https://contohenergi.co.id/"] = home
	f.web.pages["https://contohenergi.co.id/tjsl"] = csrPage()
	index := NewIndex(f.store, testInstitution(t), IndexOptions{Now: f.clock.now})
	return f, NewOnDemand(f.store, f.crawler, index, opts, quietLogger())
}

func TestOnDemand_LooksUpAnUnknownCompany(t *testing.T) {
	f, od := newOnDemandFixture(t, OnDemandOptions{})
	ctx := context.Background()

	res, err := od.LookUp(ctx, "PT Contoh Energi Tbk")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Started || !res.Complete || len(res.Prospects) != 1 {
		t.Fatalf("expected a complete lookup: %+v", res)
	}
	p := res.Prospects[0]
	if p.Profile == nil || p.Company.Domain != "contohenergi.co.id" || p.Company.Source != SourceOnDemand || p.Company.Status != StatusNew {
		t.Fatalf("expected an on-demand, unverified company with a profile: %+v", p.Company)
	}
	if len(p.Evidence) == 0 {
		t.Fatal("the answer must cite evidence")
	}

	// Asked again (or concurrently): nothing new is started.
	again, err := od.LookUp(ctx, "Contoh Energi")
	if err != nil || again.Started || len(again.Prospects) != 1 || f.extractor.calls != 1 {
		t.Fatalf("a known company must not be crawled again: %+v, %v, calls=%d", again, err, f.extractor.calls)
	}
}

func TestOnDemand_LimitsAndNames(t *testing.T) {
	_, od := newOnDemandFixture(t, OnDemandOptions{PerHour: 1})
	ctx := context.Background()
	for _, name := range []string{"PT", "Bank Indonesia Tbk", "x"} {
		if _, err := od.LookUp(ctx, name); !errors.Is(err, ErrNotACompanyName) {
			t.Errorf("%q: expected ErrNotACompanyName, got %v", name, err)
		}
	}
	if _, err := od.LookUp(ctx, "PT Contoh Energi Tbk"); err != nil {
		t.Fatal(err)
	}
	if _, err := od.LookUp(ctx, "PT Contoh Lain Tbk"); !errors.Is(err, ErrLookupLimit) {
		t.Fatalf("expected the hourly limit, got %v", err)
	}
}

func TestOnDemand_UnfinishedLookupIsLeftForTheCrawl(t *testing.T) {
	f, od := newOnDemandFixture(t, OnDemandOptions{Budget: time.Nanosecond})
	ctx := context.Background()

	res, err := od.LookUp(ctx, "PT Contoh Energi Tbk")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Started || res.Complete || len(res.Prospects) != 1 || res.Prospects[0].Profile != nil {
		t.Fatalf("expected an incomplete lookup that still recorded the company: %+v", res)
	}
	due, _ := f.store.CompaniesDue(ctx, f.clock.now(), 10)
	if len(due) != 1 || due[0].Source != SourceOnDemand {
		t.Fatalf("the company must stay due for the scheduled crawl: %+v", due)
	}
}
