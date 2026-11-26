package main

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/delivery"
	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
	"github.com/howtomakeaname/Evia/backend/internal/pipeline"
	"github.com/howtomakeaname/Evia/backend/internal/platform/config"
	"github.com/howtomakeaname/Evia/backend/internal/platform/configsign"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/telemetry"
	"github.com/howtomakeaname/Evia/backend/internal/storage/postgres"
)

// systemEventProvider adapts the embedded ingestion system catalog to the
// delivery package's reserved-event provider interface, keeping delivery free
// of any ingestion package dependency.
type systemEventProvider struct {
	catalog *ingestion.SystemCatalog
}

func (p systemEventProvider) ReservedEvents() []delivery.ReservedEvent {
	keys := p.catalog.Keys()
	events := make([]delivery.ReservedEvent, 0, len(keys))
	for _, key := range keys {
		rule, _, ok := p.catalog.Rule(key)
		if !ok {
			continue
		}
		events = append(events, delivery.ReservedEvent{
			Key:     rule.Key,
			Version: rule.Version,
			Kinds:   append([]string(nil), rule.AllowedSourceKinds...),
			Schema:  rule.Schema,
		})
	}
	return events
}

func probeKafkaConfig(c config.Config, role string) pipeline.KafkaConfig {
	out := kafkaConfig(c, role)
	out.Topic = c.Kafka.ProbeTopic
	if c.Kafka.Group != "" {
		out.Group = c.Kafka.Group + "-readiness"
	}
	return out
}

func newDelivery(c config.Config, store *postgres.Store, validator *contract.Validator, client *http.Client, role string) (*delivery.Service, *pipeline.ProbeClient, error) {
	probe, err := pipeline.NewProbeClient(probeKafkaConfig(c, role), sinkConfig(c, client))
	if err != nil {
		return nil, nil, stageError("readiness probe configuration", err)
	}
	service, err := delivery.New(store, probe, validator, delivery.Options{})
	if err != nil {
		result := stageError("release delivery configuration", err)
		closeProbe(probe, &result)
		return nil, nil, result
	}
	// A corrupt embedded catalog is a build/deployment fault; fail startup
	// instead of serving public configs that silently omit reserved events.
	systemCatalog, err := ingestion.LoadSystemCatalog()
	if err != nil {
		result := stageError("system event catalog", err)
		closeProbe(probe, &result)
		return nil, nil, result
	}
	service.WithReservedEvents(systemEventProvider{catalog: systemCatalog})
	service.WithExperiments(store)
	if c.ConfigSigningJWKPath != "" {
		signer, signerErr := configsign.NewFromFile(c.ConfigSigningJWKPath, c.ConfigSigningKeyID)
		if signerErr != nil {
			result := stageError("config signer", signerErr)
			closeProbe(probe, &result)
			return nil, nil, result
		}
		service.WithConfigSigner(signer)
	}
	if len(c.TelemetrySigningKey) > 0 {
		signer, signerErr := telemetry.New(c.TelemetrySigningKey)
		if signerErr != nil {
			result := stageError("telemetry signer", signerErr)
			closeProbe(probe, &result)
			return nil, nil, result
		}
		service.WithTelemetrySigner(signer, 5*time.Minute)
	}
	if c.ContentKeyID != "" {
		service.WithContentEncryption(&delivery.ContentEncryption{
			KeyID:     c.ContentKeyID,
			Algorithm: "RSA-OAEP-256",
			PublicJWK: c.ContentPublicJWK,
		})
	}
	return service, probe, nil
}

type probeCloser interface{ Close(context.Context) error }

func closeProbe(probe probeCloser, result *error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := probe.Close(ctx); err != nil {
		*result = errors.Join(*result, stageError("readiness probe shutdown", err))
	}
}
