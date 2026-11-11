package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/access"
	"github.com/howtomakeaname/Evia/backend/internal/analysis"
	"github.com/howtomakeaname/Evia/backend/internal/catalog"
	"github.com/howtomakeaname/Evia/backend/internal/delivery"
	"github.com/howtomakeaname/Evia/backend/internal/erasure"
	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
	"github.com/howtomakeaname/Evia/backend/internal/pipeline"
	"github.com/howtomakeaname/Evia/backend/internal/platform/budget"
	"github.com/howtomakeaname/Evia/backend/internal/platform/config"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/cursor"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/howtomakeaname/Evia/backend/internal/platform/observe"
	"github.com/howtomakeaname/Evia/backend/internal/reporting"
	"github.com/howtomakeaname/Evia/backend/internal/retention"
	"github.com/howtomakeaname/Evia/backend/internal/storage/postgres"
	"github.com/howtomakeaname/Evia/backend/internal/transport/control"
)

type failure struct {
	component string
	cause     error
}

func (e *failure) Error() string {
	return e.component + " failed; inspect dependency health and configuration"
}
func (e *failure) Unwrap() error                   { return e.cause }
func stageError(component string, err error) error { return &failure{component: component, cause: err} }

type task struct {
	name string
	run  func(context.Context) error
}
type taskResult struct {
	name string
	err  error
}

func expectedStop(ctx context.Context, err error) bool {
	return err == nil || errors.Is(err, context.Canceled) || ctx.Err() != nil && errors.Is(err, ctx.Err())
}

// supervise joins every child, treats unexpected clean exits as failures, and
// cancels siblings immediately. No worker failure is discarded in a goroutine.
func supervise(ctx context.Context, tasks []task, shutdown time.Duration) error {
	if len(tasks) == 0 {
		return errors.New("runtime requires a worker")
	}
	work, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan taskResult, len(tasks))
	for _, item := range tasks {
		go func() { results <- taskResult{item.name, item.run(work)} }()
	}
	remaining := len(tasks)
	var result error
	select {
	case <-ctx.Done():
	case first := <-results:
		remaining--
		if ctx.Err() == nil || !expectedStop(ctx, first.err) {
			result = stageError(first.name, first.err)
		}
	}
	cancel()
	timer := time.NewTimer(shutdown)
	defer timer.Stop()
	for remaining > 0 {
		select {
		case next := <-results:
			remaining--
			if !expectedStop(ctx, next.err) {
				result = errors.Join(result, stageError(next.name, next.err))
			}
		case <-timer.C:
			return errors.Join(result, errors.New("runtime shutdown exceeded its deadline"))
		}
	}
	return result
}

func kafkaConfig(c config.Config, command string) pipeline.KafkaConfig {
	out := pipeline.KafkaConfig{Brokers: c.Kafka.Brokers, Topic: c.Kafka.Topic, DeadLetterTopic: c.Kafka.DeadLetterTopic, Group: c.Kafka.Group, ClientID: c.InstanceID + "-" + command + "-" + ids.New(), Username: c.Kafka.Username, Password: c.Kafka.Password}
	if c.Kafka.TLSEnabled {
		out.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return out
}

func httpClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns, transport.MaxIdleConnsPerHost, transport.MaxConnsPerHost = 32, 16, 32
	transport.IdleConnTimeout, transport.ResponseHeaderTimeout = time.Minute, 32*time.Second
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	transport.DialContext = (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	return &http.Client{Transport: transport, Timeout: 35 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func sinkConfig(c config.Config, client *http.Client) pipeline.ClickHouseConfig {
	return pipeline.ClickHouseConfig{URL: c.ClickHouse.URL, Database: c.ClickHouse.Database, Username: c.ClickHouse.Username, Password: c.ClickHouse.Password, Client: client}
}
func queryEngine(c config.Config, client *http.Client) (*analysis.ClickHouse, error) {
	engine, err := analysis.NewClickHouse(analysis.ClickHouseConfig{URL: c.ClickHouse.URL, Database: c.ClickHouse.Database, Username: c.ClickHouse.Username, Password: c.ClickHouse.Password, Table: "event_facts", Client: client})
	if err != nil {
		return nil, stageError("analysis engine configuration", err)
	}
	return engine, nil
}
func clickHouseProbe(ctx context.Context, c config.Config, client *http.Client) error {
	endpoint, err := url.Parse(c.ClickHouse.URL)
	if err != nil {
		return stageError("ClickHouse health", err)
	}
	query := endpoint.Query()
	query.Set("database", c.ClickHouse.Database)
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), strings.NewReader("SELECT 1"))
	if err != nil {
		return stageError("ClickHouse health", err)
	}
	if c.ClickHouse.Username != "" {
		req.SetBasicAuth(c.ClickHouse.Username, c.ClickHouse.Password)
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	response, err := client.Do(req)
	if err != nil {
		return stageError("ClickHouse health", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 65))
	if err != nil || len(body) > 64 || response.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "1" {
		return stageError("ClickHouse health", errors.New("unexpected probe result"))
	}
	return nil
}
func closeProducer(producer *pipeline.KafkaProducer, result *error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := producer.Close(ctx); err != nil {
		*result = errors.Join(*result, stageError("Kafka shutdown", err))
	}
}

type analysisServicesStore interface {
	analysis.Store
	reporting.DiscoveryStore
	reporting.SavedAnalysisStore
}

// Discovery can resolve published definitions without a fact execution engine.
func configureAnalysis(api *control.API, store analysisServicesStore, validator analysis.Validator, engine analysis.Engine) {
	api.Discovery = reporting.NewDiscovery(store, validator, engine != nil)
	api.SavedAnalyses = reporting.NewSavedAnalyses(store, validator, engine != nil)
	if metrics, ok := store.(reporting.MetricStore); ok {
		api.Metrics = reporting.NewMetrics(metrics, validator, engine != nil)
	}
	if formulaMetrics, ok := store.(reporting.FormulaMetricStore); ok {
		api.FormulaMetrics = reporting.NewFormulaMetrics(formulaMetrics, validator)
	}
	if dashboards, ok := store.(reporting.DashboardStore); ok {
		api.Dashboards = reporting.NewDashboards(dashboards, validator, engine != nil)
	}
	if engine != nil {
		api.Analysis = analysis.New(store, engine, validator)
	}
}

func serve(ctx context.Context, c config.Config, store *postgres.Store, logger *slog.Logger) (result error) {
	validator, err := contract.New()
	if err != nil {
		return stageError("contract compilation", err)
	}
	codec, err := cursor.New(c.CursorKey)
	if err != nil {
		return stageError("cursor configuration", err)
	}
	accounts, err := access.New(store)
	if err != nil {
		return stageError("access service", err)
	}
	boundary := &httpapi.Boundary{Validator: validator, Logger: logger}
	api := &control.API{Boundary: boundary, Access: accounts, Machines: access.NewMachines(store), Catalog: catalog.New(store), Cursor: codec, Origin: c.Origin, SecureCookies: c.SecureCookies}
	api.Memberships = access.NewMemberships(store)
	api.Settings = access.NewSettings(store)
	api.AuditLogs = access.NewAuditLogs(store)
	api.Erasure = store
	retentionService := retention.NewServiceWithSweepReader(store, store)
	api.Retention = retentionService
	api.RetentionSweeps = retentionService
	mux := http.NewServeMux()
	probes := []func(context.Context) error{store.Pool.Ping}
	client := httpClient()
	defer client.CloseIdleConnections()
	var sources *ingestion.CachedSources
	if len(c.SourceSecretKey) > 0 || c.CollectionEnabled {
		sources = ingestion.NewCachedSources(store)
	}
	if len(c.SourceSecretKey) > 0 {
		service, err := ingestion.NewSourceService(store, c.SourceSecretKey, sources.Clear)
		if err != nil {
			return stageError("source service", err)
		}
		api.Sources = service
	}
	if c.CollectionEnabled {
		limiter, err := budget.New(c.Budget)
		if err != nil {
			return stageError("Redis budget configuration", err)
		}
		defer func() {
			if err := limiter.Close(); err != nil {
				result = errors.Join(result, stageError("Redis shutdown", err))
			}
		}()
		producer, err := pipeline.NewKafkaProducer(kafkaConfig(c, "collect"))
		if err != nil {
			return stageError("Kafka producer configuration", err)
		}
		defer closeProducer(producer, &result)
		collector, err := ingestion.NewCollector(sources, store, producer, limiter, validator)
		if err != nil {
			return stageError("collector configuration", err)
		}
		collectionAPI, err := control.NewCollectionAPI(boundary, collector)
		if err != nil {
			return stageError("collection boundary", err)
		}
		mux.Handle("/v1/collect", collectionAPI.Handler())
		probes = append(probes, limiter.Ping, producer.Ping)
	}
	var engine analysis.Engine
	if c.AnalysisEnabled {
		configuredEngine, err := queryEngine(c, client)
		if err != nil {
			return err
		}
		engine = configuredEngine
		probes = append(probes, func(ctx context.Context) error { return clickHouseProbe(ctx, c, client) })
	}
	configureAnalysis(api, store, validator, engine)
	if c.DeliveryEnabled {
		service, probe, err := newDelivery(c, store, validator, client, "readiness-api")
		if err != nil {
			return err
		}
		defer closeProbe(probe, &result)
		api.Delivery = service
		probes = append(probes, probe.Ping)
	}
	probe := func(ctx context.Context) error {
		for _, check := range probes {
			if err := check(ctx); err != nil {
				return err
			}
		}
		return nil
	}
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = probe(startup)
	cancel()
	if err != nil {
		return stageError("runtime dependency startup", err)
	}
	// The registry and instrumenter are process-local; worker role metrics are
	// a separate slice because they need domain-level queue and lease instruments.
	registry := observe.NewRegistry("serve")
	instrumenter := observe.NewInstrumenter(registry)
	// Registered without a method prefix so the handler can return an explicit
	// 405 for non-GET methods; a GET-only pattern would fall through to the API
	// catch-all and surface as 404 instead.
	mux.HandleFunc("/metrics", observe.MetricsHandler(registry))
	mux.Handle("/", api.Handler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		check, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if probe(check) != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	instrumented := instrumenter.Middleware(mux)
	bounded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		instrumented.ServeHTTP(w, r.WithContext(request))
	})
	server := httpapi.Server(c.Listen, bounded)
	logger.Info("API starting", "listen", c.Listen, "collection_enabled", c.CollectionEnabled, "analysis_enabled", c.AnalysisEnabled, "delivery_enabled", c.DeliveryEnabled, "sources_enabled", len(c.SourceSecretKey) > 0)
	return supervise(ctx, []task{{"HTTP listener", func(ctx context.Context) error { return runHTTP(ctx, server) }}, {"credential cleanup", func(ctx context.Context) error { return cleanup(ctx, store) }}}, 15*time.Second)
}

func runHTTP(ctx context.Context, server *http.Server) error {
	result := make(chan error, 1)
	go func() { result <- server.ListenAndServe() }()
	select {
	case err := <-result:
		if ctx.Err() != nil && errors.Is(err, http.ErrServerClosed) {
			return ctx.Err()
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			server.Close()
			return stageError("HTTP shutdown", err)
		}
		select {
		case err := <-result:
			if !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return ctx.Err()
		case <-shutdown.Done():
			server.Close()
			return stageError("HTTP shutdown", shutdown.Err())
		}
	}
}
func cleanup(ctx context.Context, store *postgres.Store) error {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			clean, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := store.Cleanup(clean)
			cancel()
			if err != nil {
				return err
			}
		}
	}
}

// metricsTasks returns the administrative metrics/health listener task for a
// worker process when EVIA_METRICS_LISTEN is configured, and nil otherwise.
// Workers have no business HTTP listener, so this is their only liveness and
// observability endpoint; the role label distinguishes processes in scrapes.
// The same registry is shared with domain workers so their instruments appear
// on the worker's own scrape endpoint.
func metricsTasks(c config.Config, role string, registry *observe.Registry) []task {
	if c.MetricsListen == "" {
		return nil
	}
	server := observe.NewServer(c.MetricsListen, registry)
	return []task{{name: role + " metrics listener", run: func(ctx context.Context) error {
		return observe.RunServer(ctx, server)
	}}}
}

func ingestWorker(ctx context.Context, c config.Config, store *postgres.Store, logger *slog.Logger) (result error) {
	client := httpClient()
	defer client.CloseIdleConnections()
	producer, err := pipeline.NewKafkaProducer(kafkaConfig(c, "quarantine"))
	if err != nil {
		return stageError("Kafka producer configuration", err)
	}
	defer closeProducer(producer, &result)
	sink, err := pipeline.NewClickHouse(sinkConfig(c, client))
	if err != nil {
		return stageError("fact sink configuration", err)
	}
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = producer.Ping(startup)
	if err == nil {
		err = clickHouseProbe(startup, c, client)
	}
	cancel()
	if err != nil {
		return stageError("ingestion worker startup", err)
	}
	tasks := []task{}
	if c.DeliveryEnabled {
		validator, err := contract.New()
		if err != nil {
			return stageError("contract compilation", err)
		}
		service, probe, err := newDelivery(c, store, validator, client, "readiness-renewal")
		if err != nil {
			return err
		}
		defer closeProbe(probe, &result)
		worker, err := pipeline.NewProbeWorker(probeKafkaConfig(c, "readiness-worker"), sinkConfig(c, client))
		if err != nil {
			return stageError("readiness worker configuration", err)
		}
		defer closeProbe(worker, &result)
		startup, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = probe.Ping(startup)
		if err == nil {
			err = worker.Ping(startup)
		}
		cancel()
		if err != nil {
			return stageError("readiness worker startup", err)
		}
		renewal := &delivery.Worker{Service: service, Interval: 10 * time.Second, Logger: logger}
		tasks = append(tasks, task{"readiness receipt worker", worker.Run}, task{"release readiness renewal", renewal.Run})
	}
	reader, err := pipeline.NewKafkaReader(kafkaConfig(c, "materialize"))
	if err != nil {
		return stageError("Kafka reader configuration", err)
	}
	// The tombstone barrier wraps only the fact sink; the readiness probe
	// worker above intentionally keeps its unfiltered sink.
	guardedSink := pipeline.NewBarrierSink(sink, store)
	registry := observe.NewRegistry("ingest-worker")
	worker := &pipeline.Worker{
		Reader:       reader,
		Sink:         guardedSink,
		Quarantine:   producer,
		Observer:     newIngestMetricsObserver(registry),
		BatchSize:    128,
		BatchTimeout: 30 * time.Second,
		Logger:       logger,
	}
	logger.Info("ingestion worker starting", "delivery_enabled", c.DeliveryEnabled)
	tasks = append(tasks, task{"fact ingestion worker", worker.Run})
	if c.IngestLagInterval > 0 {
		lagMonitor := &pipeline.LagMonitor{
			Reader:   reader,
			Observer: newIngestLagObserver(registry),
			Interval: c.IngestLagInterval,
			Logger:   logger,
		}
		tasks = append(tasks, task{"consumer lag monitor", lagMonitor.Run})
	}
	return supervise(ctx, append(metricsTasks(c, "ingest-worker", registry), tasks...), 15*time.Second)
}

func queryWorkers(ctx context.Context, c config.Config, store *postgres.Store, logger *slog.Logger) error {
	client := httpClient()
	defer client.CloseIdleConnections()
	engine, err := queryEngine(c, client)
	if err != nil {
		return err
	}
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = clickHouseProbe(startup, c, client)
	cancel()
	if err != nil {
		return stageError("query worker startup", err)
	}
	processID := c.InstanceID + "-" + ids.New()
	// One registry and observer are shared by the whole pool so counters
	// aggregate across every configured query worker.
	registry := observe.NewRegistry("query-worker")
	observer := newQueryMetricsObserver(registry)
	tasks := make([]task, 0, c.QueryWorkers)
	// Route internal formula jobs to a bounded fan-out; ordinary requests pass
	// through to the ClickHouse engine unchanged.
	var executionEngine analysis.Engine = reporting.NewCompositeFormulaEngine(engine, store)
	for i := range c.QueryWorkers {
		worker := analysis.NewWorker(store, executionEngine, processID+"-"+strconv.Itoa(i), observer)
		tasks = append(tasks, task{name: fmt.Sprintf("query worker %d", i), run: worker.Run})
	}
	logger.Info("query workers starting", "count", c.QueryWorkers)
	return supervise(ctx, append(metricsTasks(c, "query-worker", registry), tasks...), 15*time.Second)
}

func erasureWorkerRuntime(ctx context.Context, c config.Config, store *postgres.Store, logger *slog.Logger) error {
	client := httpClient()
	defer client.CloseIdleConnections()
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	err := clickHouseProbe(startup, c, client)
	cancel()
	if err != nil {
		return stageError("erasure worker startup", err)
	}
	executor, err := pipeline.NewErasureExecutor(sinkConfig(c, client), client)
	if err != nil {
		return stageError("erasure executor configuration", err)
	}
	registry := observe.NewRegistry("erasure-worker")
	worker := &erasure.Worker{
		Store:    store,
		Deleter:  executor,
		Observer: newErasureMetricsObserver(registry),
		ID:       c.InstanceID + "-" + ids.New(),
		Lease:    2 * time.Minute,
		Logger:   logger,
	}
	logger.Info("erasure worker starting")
	return supervise(ctx, append(metricsTasks(c, "erasure-worker", registry), task{name: "erasure worker", run: worker.Run}), 15*time.Second)
}

func retentionWorkerRuntime(ctx context.Context, c config.Config, store *postgres.Store, logger *slog.Logger) error {
	client := httpClient()
	defer client.CloseIdleConnections()
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	err := clickHouseProbe(startup, c, client)
	cancel()
	if err != nil {
		return stageError("retention worker startup", err)
	}
	executor, err := pipeline.NewRetentionExecutor(sinkConfig(c, client), client)
	if err != nil {
		return stageError("retention executor configuration", err)
	}
	mode := "dry-run"
	if c.RetentionEnforcement {
		mode = "enforcement"
	}
	registry := observe.NewRegistry("retention-worker")
	worker := retention.NewSweepWorker(store, executor, c.RetentionEnforcement, c.RetentionSweepInterval, logger, newRetentionSweepObserver(registry))
	logger.Info("retention worker starting", "mode", mode, "interval", c.RetentionSweepInterval.String())
	return supervise(ctx, append(metricsTasks(c, "retention-worker", registry), task{name: "retention worker", run: worker.Run}), 15*time.Second)
}
