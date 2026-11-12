// evia-catalog-migration turns a reviewed hot-key catalog JSON file into the
// additive ClickHouse migration that creates its slot columns and recreates
// the event_facts view. It performs no I/O against ClickHouse: the emitted
// SQL is reviewed and applied by an operator, matching the manual-migration
// discipline of deploy/clickhouse.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/howtomakeaname/Evia/backend/internal/propertycatalog"
)

func main() { os.Exit(command(os.Args[1:], os.Stdout, os.Stderr)) }

func command(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("evia-catalog-migration", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("catalog", os.Getenv("EVIA_PROPERTY_CATALOG_PATH"), "reviewed hot-key catalog JSON path")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *path == "" {
		fmt.Fprintln(stderr, "require --catalog or EVIA_PROPERTY_CATALOG_PATH and no positional arguments")
		return 2
	}
	catalog, err := propertycatalog.LoadFile(*path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if _, err := io.WriteString(stdout, catalog.MigrationSQL()); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
