package main

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/delivery"
	"github.com/howtomakeaname/Evia/backend/internal/pipeline"
	"github.com/howtomakeaname/Evia/backend/internal/platform/config"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/storage/postgres"
)

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
