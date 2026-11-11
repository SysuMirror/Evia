package main

import (
	"strconv"

	"github.com/howtomakeaname/Evia/backend/internal/pipeline"
	"github.com/howtomakeaname/Evia/backend/internal/platform/observe"
)

// ingestLagObserver adapts partition lag samples to Prometheus gauges on the
// ingest-worker registry. Labels identify the fixed topic/partition set, so
// cardinality is bounded by the Kafka partition count rather than traffic.
type ingestLagObserver struct {
	lag       *observe.Gauge
	committed *observe.Gauge
	logEnd    *observe.Gauge
}

func newIngestLagObserver(registry *observe.Registry) pipeline.LagObserver {
	return &ingestLagObserver{
		lag: registry.Gauge("evia_ingest_consumer_lag",
			"Messages between the consumer group position and the broker log end offset per partition.",
			"topic", "partition"),
		committed: registry.Gauge("evia_ingest_consumer_committed_offset",
			"Consumer group consume position; the next offset the group will read.",
			"topic", "partition"),
		logEnd: registry.Gauge("evia_ingest_consumer_log_end_offset",
			"Broker log end offset (high watermark) for the consumed partition.",
			"topic", "partition"),
	}
}

func (o *ingestLagObserver) PartitionLagObserved(sample pipeline.PartitionLag) {
	partition := strconv.Itoa(int(sample.Partition))
	o.committed.Set(float64(sample.CommittedOffset), sample.Topic, partition)
	o.logEnd.Set(float64(sample.LogEndOffset), sample.Topic, partition)
	o.lag.Set(float64(sample.Lag), sample.Topic, partition)
}
