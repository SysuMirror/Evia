package postgres

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestMultiHostDSNBuildsFailoverPool proves that a DSN accepted by the config
// contract is parsed by the real pool constructor into ordered fallback
// hosts with read-write session validation, without performing any network
// I/O. The pool itself is never dialed here.
func TestMultiHostDSNBuildsFailoverPool(t *testing.T) {
	dsn := "postgres://evia:secret@pg-1:5432,pg-2:5433,pg-3:5434/evia?sslmode=disable&target_session_attrs=read-write"
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("validated multi-host DSN rejected by pgxpool: %v", err)
	}
	if cfg.ConnConfig.Host != "pg-1" || cfg.ConnConfig.Port != 5432 {
		t.Fatalf("primary host not selected first: %s:%d", cfg.ConnConfig.Host, cfg.ConnConfig.Port)
	}
	if cfg.ConnConfig.ValidateConnect == nil {
		t.Fatal("target_session_attrs=read-write must install a connect validator")
	}
	hosts := map[string]uint16{cfg.ConnConfig.Host: cfg.ConnConfig.Port}
	for _, fallback := range cfg.ConnConfig.Fallbacks {
		hosts[fallback.Host] = fallback.Port
	}
	for _, want := range []struct {
		host string
		port uint16
	}{{"pg-1", 5432}, {"pg-2", 5433}, {"pg-3", 5434}} {
		if got, ok := hosts[want.host]; !ok || got != want.port {
			t.Fatalf("fallback host %s:%d missing from pool config: %+v", want.host, want.port, hosts)
		}
	}
}

func TestSingleHostDSNKeepsNoExplicitValidator(t *testing.T) {
	cfg, err := pgxpool.ParseConfig("postgres://evia:secret@127.0.0.1:55432/evia_fullstack?sslmode=disable")
	if err != nil {
		t.Fatalf("development DSN rejected by pgxpool: %v", err)
	}
	if cfg.ConnConfig.ValidateConnect != nil {
		t.Fatal("the default single-host DSN must keep its historical implicit session behavior")
	}
	if cfg.ConnConfig.Host != "127.0.0.1" || cfg.ConnConfig.Port != 55432 {
		t.Fatalf("single-host DSN parsed incorrectly: %s:%d", cfg.ConnConfig.Host, cfg.ConnConfig.Port)
	}
}
