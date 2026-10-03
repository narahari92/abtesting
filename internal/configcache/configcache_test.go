package configcache

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var discard = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

func TestFileSourceSample(t *testing.T) {
	configs, err := FileSource{Path: "testdata/config.sample.json"}.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 2 || configs[0].Site.Key != "demo" {
		t.Fatalf("unexpected configs: %+v", configs)
	}
	if configs[0].Site.PayloadVersion == 0 {
		t.Error("version should be derived from content")
	}
	s := Build(configs, time.Now(), discard)
	demo := s.Sites["demo"]
	if demo == nil || len(demo.Payload.Experiments) != 2 {
		t.Fatalf("demo payload should have 2 running experiments: %+v", demo)
	}
	if demo.Payload.Find("pricing-copy") != nil {
		t.Error("draft experiment leaked into payload")
	}
	if demo.ETag == "" || demo.ETag != demo.Payload.ETag() || len(demo.Bytes) == 0 {
		t.Error("entry bytes/etag not set")
	}
	if sus := s.Sites["suspended-site"]; sus == nil || len(sus.Payload.Experiments) != 0 {
		t.Error("suspended site must get an empty payload")
	}
}

func TestFileSourceVersionTracksContent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.json")
	write := func(s string) {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"sites":[{"site":{"key":"a"},"experiments":[]}]}`)
	v1, _ := FileSource{p}.Load(context.Background())
	v1again, _ := FileSource{p}.Load(context.Background())
	write(`{"sites":[{"site":{"key":"a","name":"renamed"},"experiments":[]}]}`)
	v2, _ := FileSource{p}.Load(context.Background())
	if v1[0].Site.PayloadVersion != v1again[0].Site.PayloadVersion {
		t.Error("same content must give same version")
	}
	if v1[0].Site.PayloadVersion == v2[0].Site.PayloadVersion {
		t.Error("changed content must change version")
	}
	if v1[0].Site.Status != "active" {
		t.Error("status should default to active")
	}
}

func TestFileSourceErrors(t *testing.T) {
	if _, err := (FileSource{"testdata/missing.json"}).Load(context.Background()); err == nil {
		t.Error("missing file must error")
	}
	p := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(p, []byte(`{"sites":[{"experiments":[]}]}`), 0o644)
	if _, err := (FileSource{p}).Load(context.Background()); err == nil {
		t.Error("site without key must error")
	}
	os.WriteFile(p, []byte(`not json`), 0o644)
	if _, err := (FileSource{p}).Load(context.Background()); err == nil {
		t.Error("invalid json must error")
	}
}

type failingSource struct{ err error }

func (f failingSource) Load(context.Context) ([]SiteConfig, error) { return nil, f.err }

func TestRefreshKeepsLastGood(t *testing.T) {
	var c Cache
	if c.Current() != nil || c.Get("demo") != nil {
		t.Fatal("cache should start empty")
	}
	if err := c.Refresh(context.Background(), FileSource{"testdata/config.sample.json"}, discard); err != nil {
		t.Fatal(err)
	}
	before := c.Current()
	err := c.Refresh(context.Background(), failingSource{errors.New("db down")}, discard)
	if err == nil {
		t.Fatal("expected error")
	}
	if c.Current() != before || c.Get("demo") == nil {
		t.Fatal("failed refresh must keep the previous snapshot")
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	var c Cache
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.Run(ctx, FileSource{"testdata/config.sample.json"}, 10*time.Millisecond, discard)
		close(done)
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop")
	}
	if c.Get("demo") == nil {
		t.Fatal("Run should have loaded the snapshot")
	}
}
