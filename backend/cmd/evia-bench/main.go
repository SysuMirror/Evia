// evia-bench submits synthetic benchmark events through the real public API.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() { os.Exit(command(os.Args[1:])) }

func command(args []string) int {
	flags := flag.NewFlagSet("evia-bench", flag.ContinueOnError)
	var o Options
	path := flags.String("config", os.Getenv("EVIA_BENCH_CONFIG"), "private mode-0600 JSON configuration path")
	flags.IntVar(&o.Events, "events", 0, "required unique synthetic event count (1..10000000)")
	flags.IntVar(&o.BatchSize, "batch-size", 50, "events per request (1..50)")
	flags.IntVar(&o.Concurrency, "concurrency", 1, "maximum in-flight HTTP requests (1..128)")
	flags.Float64Var(&o.Rate, "rate", 0, "target events/second; 0 uses closed loop concurrency")
	flags.IntVar(&o.ReplayPercent, "replay-percent", 0, "replay this percentage of original events after the unique phase")
	flags.StringVar(&o.Out, "out", "", "required atomic JSON evidence output path")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *path == "" {
		fmt.Fprintln(os.Stderr, "require --config or EVIA_BENCH_CONFIG and no positional arguments")
		return 2
	}
	if err := o.validate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	c, err := readConfig(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, deadlineCancel := context.WithTimeout(ctx, 24*time.Minute)
	defer deadlineCancel()
	r := run(ctx, c, o)
	if writeReport(o.Out, r) != nil {
		fmt.Fprintln(os.Stderr, "cannot atomically write benchmark evidence")
		return 1
	}
	fmt.Fprintf(os.Stdout, "complete=%t unique_accepted=%d replay_accepted=%d requests=%d elapsed_seconds=%.3f\n", r.Complete, r.Unique.Accepted, r.Replay.Accepted, r.Requests, r.Elapsed)
	if !r.Complete {
		return 1
	}
	return 0
}
