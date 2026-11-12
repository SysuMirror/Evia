package main

import (
	"net/http"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/platform/config"
	"github.com/howtomakeaname/Evia/backend/internal/propertycatalog"
)

func TestSinkConfigForwardsPropertyCatalog(t *testing.T) {
	catalog := &propertycatalog.Catalog{Revision: 2, Entries: []propertycatalog.Entry{
		{EventKey: "page_opened", SchemaVersion: 1, PropertyKey: "title", ResolvedType: "string"},
	}}
	if err := catalog.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		ClickHouse: config.ClickHouse{
			URL: "http://example.invalid", Database: "evia", Username: "u", Password: "p",
		},
		PropertyCatalog: catalog,
	}
	sink := sinkConfig(cfg, &http.Client{})
	if sink.Catalog != catalog {
		t.Fatal("sinkConfig must forward the parsed property catalog to the pipeline sink")
	}
}

func TestQueryEngineBuildsWithPropertyCatalog(t *testing.T) {
	catalog := &propertycatalog.Catalog{Revision: 4, Entries: []propertycatalog.Entry{
		{EventKey: "page_opened", SchemaVersion: 1, PropertyKey: "score", ResolvedType: "integer"},
	}}
	if err := catalog.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		ClickHouse: config.ClickHouse{
			URL: "http://example.invalid", Database: "evia", Username: "u", Password: "p",
		},
		PropertyCatalog: catalog,
	}
	// queryEngine only parses the endpoint, so construction proves the wiring
	// without opening a connection; a nil catalog would be normalized inside.
	engine, err := queryEngine(cfg, &http.Client{})
	if err != nil {
		t.Fatal(err)
	}
	if engine == nil {
		t.Fatal("queryEngine returned a nil engine")
	}
}

func TestDisabledCatalogStillBuildsSinkAndEngine(t *testing.T) {
	// The zero-value config carries no catalog path, mirroring production
	// defaults; both construction paths must remain usable without one.
	cfg := config.Config{
		ClickHouse: config.ClickHouse{
			URL: "http://example.invalid", Database: "evia", Username: "u", Password: "p",
		},
	}
	sink := sinkConfig(cfg, &http.Client{})
	if sink.Catalog != nil && sink.Catalog.Enabled() {
		t.Fatal("default config must not enable a catalog on the sink")
	}
	engine, err := queryEngine(cfg, &http.Client{})
	if err != nil || engine == nil {
		t.Fatalf("default config must still build a query engine: %v", err)
	}
}
