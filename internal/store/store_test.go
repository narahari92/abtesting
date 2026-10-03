package store_test

import (
	"context"
	"errors"
	"testing"

	"variantsvc/internal/experiment"
	"variantsvc/internal/store"
	"variantsvc/internal/store/storetest"
)

func ctx() context.Context { return context.Background() }

func sampleExperiment(key string) experiment.Experiment {
	return experiment.Experiment{
		Key: key, Name: "Test " + key, Status: experiment.StatusDraft, Seed: "seed-" + key, HashVersion: 1, CoverageBP: 10000,
		Variants: []experiment.Variant{
			{Key: "control", WeightBP: 5000, IsControl: true, Source: experiment.SourceManual, Approved: true, Position: 0, Content: map[string]any{"headline": "A"}},
			{Key: "b", WeightBP: 5000, Source: experiment.SourceManual, Approved: true, Position: 1, Content: map[string]any{"headline": "B", "n": float64(2)}},
		},
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := storetest.Open(t)
	if err := s.Migrate(ctx()); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	applied, err := s.AppliedMigrations(ctx())
	if err != nil || len(applied) == 0 || applied[0] != "0001_init" {
		t.Fatalf("applied = %v, err = %v", applied, err)
	}
}

func TestSitesCRUD(t *testing.T) {
	s := storetest.Open(t)
	site, err := s.CreateSite(ctx(), experiment.Site{Key: "acme", Name: "Acme", APIKeyHash: "hash-1", AllowedOrigins: []string{"https://www.acme.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if site.ID == "" || site.Status != experiment.SiteActive || site.PayloadVersion != 1 || site.AssignRPSLimit != 2000 || site.EventsRPSLimit != 500 {
		t.Fatalf("unexpected site: %+v", site)
	}
	if _, err := s.CreateSite(ctx(), experiment.Site{Key: "acme", APIKeyHash: "hash-2"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate key: %v", err)
	}
	if _, err := s.CreateSite(ctx(), experiment.Site{Key: "other", APIKeyHash: "hash-1"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate hash: %v", err)
	}
	got, err := s.GetSiteByAPIKeyHash(ctx(), "hash-1")
	if err != nil || got.Key != "acme" {
		t.Fatalf("by hash: %+v %v", got, err)
	}
	if _, err := s.GetSite(ctx(), "ghost"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing site: %v", err)
	}

	upd, err := s.UpdateSiteSettings(ctx(), "acme", "Acme Inc", []string{"https://a.com", "https://b.com"})
	if err != nil || upd.Name != "Acme Inc" || len(upd.AllowedOrigins) != 2 || upd.PayloadVersion != 1 {
		t.Fatalf("settings update: %+v %v (version must not bump)", upd, err)
	}
	sus, err := s.SetSiteStatus(ctx(), "acme", experiment.SiteSuspended)
	if err != nil || sus.Status != experiment.SiteSuspended || sus.PayloadVersion != 2 {
		t.Fatalf("suspend: %+v %v", sus, err)
	}
	rot, err := s.RotateSiteKey(ctx(), "acme", "hash-3")
	if err != nil || rot.APIKeyHash != "hash-3" {
		t.Fatalf("rotate: %v", err)
	}
	if _, err := s.GetSiteByAPIKeyHash(ctx(), "hash-1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("old hash must stop resolving")
	}
	if err := s.DeleteSite(ctx(), "acme"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSite(ctx(), "acme"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestExperimentsCRUDAndVersionBump(t *testing.T) {
	s := storetest.Open(t)
	site, _ := s.CreateSite(ctx(), experiment.Site{Key: "acme", APIKeyHash: "h"})

	e, err := s.CreateExperiment(ctx(), site.ID, sampleExperiment("hero"))
	if err != nil {
		t.Fatal(err)
	}
	if e.ID == "" || e.SiteID != site.ID || len(e.Variants) != 2 || e.Variants[0].Key != "control" || e.Variants[1].Content["n"] != float64(2) {
		t.Fatalf("created: %+v", e)
	}
	after, _ := s.GetSite(ctx(), "acme")
	if after.PayloadVersion != site.PayloadVersion+1 {
		t.Fatalf("create must bump payload_version: %d -> %d", site.PayloadVersion, after.PayloadVersion)
	}
	if _, err := s.CreateExperiment(ctx(), site.ID, sampleExperiment("hero")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate experiment key: %v", err)
	}

	e.Status = experiment.StatusRunning
	e.CoverageBP = 9000
	e.Variants[1].Content["headline"] = "B2"
	saved, err := s.SaveExperiment(ctx(), site.ID, e)
	if err != nil || saved.Status != experiment.StatusRunning || saved.CoverageBP != 9000 || saved.Variants[1].Content["headline"] != "B2" {
		t.Fatalf("save: %+v %v", saved, err)
	}
	after2, _ := s.GetSite(ctx(), "acme")
	if after2.PayloadVersion != after.PayloadVersion+1 {
		t.Fatal("save must bump payload_version")
	}

	list, err := s.ListExperiments(ctx(), site.ID)
	if err != nil || len(list) != 1 || len(list[0].Variants) != 2 {
		t.Fatalf("list: %v %v", list, err)
	}
	if _, err := s.GetExperiment(ctx(), site.ID, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing experiment: %v", err)
	}
	ghost := sampleExperiment("x")
	ghost.ID = e.ID
	if _, err := s.SaveExperiment(ctx(), "00000000-0000-0000-0000-000000000000", ghost); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("save for wrong site: %v", err)
	}
}

func TestTenantIsolationInStore(t *testing.T) {
	s := storetest.Open(t)
	a, _ := s.CreateSite(ctx(), experiment.Site{Key: "a", APIKeyHash: "ha"})
	b, _ := s.CreateSite(ctx(), experiment.Site{Key: "b", APIKeyHash: "hb"})
	if _, err := s.CreateExperiment(ctx(), a.ID, sampleExperiment("shared-key")); err != nil {
		t.Fatal(err)
	}
	// Same experiment key on another site is fine: uniqueness is per site.
	if _, err := s.CreateExperiment(ctx(), b.ID, sampleExperiment("shared-key")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetExperiment(ctx(), b.ID, "only-a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("cross-site read must be not found")
	}
	listB, _ := s.ListExperiments(ctx(), b.ID)
	if len(listB) != 1 || listB[0].SiteID != b.ID {
		t.Fatalf("site b sees %d experiments", len(listB))
	}
	sites, bySite, err := s.LoadAll(ctx())
	if err != nil || len(sites) != 2 || len(bySite[a.ID]) != 1 || len(bySite[b.ID]) != 1 {
		t.Fatalf("LoadAll: %d sites, %v, %v", len(sites), bySite, err)
	}
	if err := s.DeleteSite(ctx(), "a"); err != nil {
		t.Fatal(err)
	}
	_, bySite, _ = s.LoadAll(ctx())
	if len(bySite[a.ID]) != 0 || len(bySite[b.ID]) != 1 {
		t.Fatal("delete must cascade to experiments of that site only")
	}
}
