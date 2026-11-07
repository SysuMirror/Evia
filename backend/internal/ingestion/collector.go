package ingestion

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type Collector struct {
	sources   SourceResolver
	plans     PlanResolver
	queue     Queue
	budget    Budget
	validator SchemaValidator
	mu        sync.Mutex
	cache     map[string]compiledPlan
}
type compiledPlan struct {
	plan    Plan
	schemas map[string]*jsonschema.Schema
	expires time.Time
}

func NewCollector(sources SourceResolver, plans PlanResolver, queue Queue, budget Budget, validator SchemaValidator) (*Collector, error) {
	if sources == nil || plans == nil || queue == nil || budget == nil || validator == nil {
		return nil, errors.New("collector requires source, plan, durable queue, global budget and contract adapters")
	}
	return &Collector{sources: sources, plans: plans, queue: queue, budget: budget, validator: validator, cache: map[string]compiledPlan{}}, nil
}
func unavailable(code string) error {
	return fault.New(fault.Unavailable, code, "接收服务暂不可用，请保留原事件并稍后重试")
}
func (c *Collector) plan(ctx context.Context, project, env string, revision int64) (compiledPlan, error) {
	key := fmt.Sprintf("%s:%s:%d", project, env, revision)
	now := time.Now()
	c.mu.Lock()
	cached, ok := c.cache[key]
	c.mu.Unlock()
	if ok && cached.expires.After(now) {
		return cached, nil
	}
	p, e := c.plans.ResolvePlan(ctx, project, env, revision)
	if e != nil {
		return compiledPlan{}, e
	}
	if p.ProjectID != project || p.Environment != env || p.Revision != revision {
		return compiledPlan{}, unavailable("EVENT_PLAN_SCOPE_MISMATCH")
	}
	if !p.Ready || !p.Active {
		return compiledPlan{}, unavailable("EVENT_PLAN_NOT_READY")
	}
	compiled := compiledPlan{plan: p, schemas: map[string]*jsonschema.Schema{}, expires: now.Add(2 * time.Second)}
	if len(p.Events) > 200 {
		return compiledPlan{}, unavailable("EVENT_PLAN_INVALID")
	}
	for key, rule := range p.Events {
		compiler := jsonschema.NewCompiler()
		const uri = "https://schemas.evia.invalid/ingestion/properties"
		if e = compiler.AddResource(uri, rule.Schema); e != nil {
			return compiledPlan{}, unavailable("EVENT_PLAN_INVALID")
		}
		schema, e := compiler.Compile(uri)
		if e != nil {
			return compiledPlan{}, unavailable("EVENT_PLAN_INVALID")
		}
		compiled.schemas[key] = schema
	}
	c.mu.Lock()
	if len(c.cache) >= 4096 {
		for k, v := range c.cache {
			if !v.expires.After(now) {
				delete(c.cache, k)
			}
		}
		if len(c.cache) >= 4096 {
			for k := range c.cache {
				delete(c.cache, k)
				break
			}
		}
	}
	c.cache[key] = compiled
	c.mu.Unlock()
	return compiled, nil
}

func (c *Collector) authenticate(ctx context.Context, key string, auth Authentication) (AdmissionSource, error) {
	if len(key) < 1 || len(key) > 128 {
		return AdmissionSource{}, fault.New(fault.Unauthenticated, "SOURCE_CREDENTIAL_INVALID", "来源凭据无效")
	}
	source, e := c.sources.ResolveSource(ctx, key)
	if e != nil {
		if fault.Public(e).Kind == fault.Unauthenticated {
			return source, e
		}
		return source, unavailable("SOURCE_CONFIGURATION_UNAVAILABLE")
	}
	if source.Source.Status != "active" {
		return source, fault.New(fault.Forbidden, "SOURCE_DISABLED", "来源已停用")
	}
	if source.Credential.ExpiresAt != nil && !source.Credential.ExpiresAt.After(time.Now()) {
		return source, fault.New(fault.Unauthenticated, "SOURCE_CREDENTIAL_INVALID", "来源凭据已失效")
	}
	if source.Source.Kind == "server" {
		if len(auth.Bearer) != 43 || subtle.ConstantTimeCompare([]byte(Digest(auth.Bearer)), []byte(source.Credential.SecretDigest)) != 1 {
			return source, fault.New(fault.Unauthenticated, "SOURCE_CREDENTIAL_INVALID", "来源凭据无效")
		}
	} else if source.Source.Kind == "browser" {
		allowed := false
		for _, origin := range source.Source.AllowedOrigins {
			allowed = allowed || origin == auth.Origin
		}
		if auth.Origin == "" || !allowed {
			return source, fault.New(fault.Forbidden, "SOURCE_ORIGIN_FORBIDDEN", "该来源不允许此 Origin")
		}
		if auth.Bearer != "" {
			return source, fault.New(fault.Forbidden, "BROWSER_AUTH_FORBIDDEN", "浏览器来源不得携带网站登录凭据")
		}
	} else {
		return source, unavailable("SOURCE_CONFIGURATION_INVALID")
	}
	return source, nil
}

// Collect validates each item independently and acknowledges only after Publish
// confirms the accepted subset durably. Retries preserve original event IDs.
func (c *Collector) Collect(ctx context.Context, batch Batch, auth Authentication) (Receipt, int, error) {
	if !uuidPattern.MatchString(batch.ID) || batch.SentAt.IsZero() || len(batch.Events) < 1 || len(batch.Events) > 50 {
		return Receipt{}, 0, Invalid("BAD_BATCH", "批次结构无效")
	}
	now := time.Now().UTC()
	if batch.SentAt.Before(now.Add(-time.Hour)) || batch.SentAt.After(now.Add(5*time.Minute)) {
		return Receipt{}, 0, Invalid("BAD_BATCH_TIME", "批次发送时间超出实时接收窗口")
	}
	source, e := c.authenticate(ctx, batch.WriteKey, auth)
	if e != nil {
		return Receipt{}, 0, e
	}
	if e = c.budget.ReserveCollection(ctx, source.Source.ProjectID, source.Source.Environment, len(batch.Events)); e != nil {
		if fault.Public(e).Kind == fault.Limited {
			return Receipt{}, 0, e
		}
		return Receipt{}, 0, unavailable("ADMISSION_BUDGET_UNAVAILABLE")
	}
	out := Receipt{BatchID: batch.ID, ReceiptID: ids.New(), Results: make([]ItemResult, len(batch.Events))}
	records := make([]Record, 0, len(batch.Events))
	retryable := false
	// Every distinct version is fetched at most once within this batch. Successful
	// compiled manifests are shared across batches with a bounded staleness TTL.
	batchPlans := map[int64]compiledPlan{}
	planErrors := map[int64]error{}
	for i, raw := range batch.Events {
		result := ItemResult{Index: i, Status: "rejected"}
		reject := func(code string, retry bool, paths ...string) {
			result.Error = &ItemError{Code: code, Retryable: retry, FieldPaths: paths}
			retryable = retryable || retry
		}
		if len(raw) > 8*1024 {
			reject("EVENT_TOO_LARGE", false)
			out.Results[i] = result
			continue
		}
		value, e := contract.Decode(raw)
		if e != nil || c.validator.Validate("EventEnvelope", value) != nil {
			reject("INVALID_EVENT", false)
			out.Results[i] = result
			continue
		}
		var event struct {
			ID       string    `json:"event_id"`
			Key      string    `json:"event_key"`
			Version  int64     `json:"schema_version"`
			Revision int64     `json:"release_revision"`
			Occurred time.Time `json:"occurred_at"`
			Identity struct {
				UID         string `json:"uid"`
				AnonymousID string `json:"anonymous_id"`
			} `json:"identity"`
			Context json.RawMessage `json:"context"`
			SDK     struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"sdk"`
			Properties json.RawMessage `json:"properties"`
		}
		if e = json.Unmarshal(raw, &event); e != nil {
			reject("INVALID_EVENT", false)
			out.Results[i] = result
			continue
		}
		result.EventID = event.ID
		if event.Occurred.Before(now.Add(-time.Hour)) {
			reject("EVENT_TOO_OLD", false, "occurred_at")
			out.Results[i] = result
			continue
		}
		if event.Occurred.After(now.Add(5 * time.Minute)) {
			reject("EVENT_TIME_IN_FUTURE", false, "occurred_at")
			out.Results[i] = result
			continue
		}
		plan, ok := batchPlans[event.Revision]
		if !ok {
			if _, seen := planErrors[event.Revision]; !seen {
				// Bound adversarial cache-miss amplification independently of batch size.
				if len(batchPlans)+len(planErrors) >= 8 {
					planErrors[event.Revision] = unavailable("SCHEMA_NOT_READY")
				} else {
					plan, e = c.plan(ctx, source.Source.ProjectID, source.Source.Environment, event.Revision)
					if e != nil {
						planErrors[event.Revision] = e
					} else {
						batchPlans[event.Revision] = plan
					}
				}
			}
			plan = batchPlans[event.Revision]
		}
		if e := planErrors[event.Revision]; e != nil {
			f := fault.Public(e)
			if f.Kind == fault.Invalid {
				reject(f.Code, false, "release_revision")
			} else {
				code := f.Code
				if f.Kind == fault.Internal {
					code = "EVENT_PLAN_UNAVAILABLE"
				}
				reject(code, true, "release_revision")
			}
			out.Results[i] = result
			continue
		}
		rule, ok := plan.plan.Events[event.Key]
		if !ok || rule.Version != event.Version {
			reject("SCHEMA_NOT_PUBLISHED", false, "schema_version")
			out.Results[i] = result
			continue
		}
		allowed := false
		for _, kind := range rule.AllowedSourceKinds {
			allowed = allowed || kind == source.Source.Kind
		}
		if !allowed {
			reject("SOURCE_KIND_FORBIDDEN", false)
			out.Results[i] = result
			continue
		}
		properties, e := contract.Decode(event.Properties)
		if e != nil || plan.schemas[event.Key].Validate(properties) != nil {
			reject("PROPERTY_SCHEMA_MISMATCH", false, "properties")
			out.Results[i] = result
			continue
		}
		var eventContext EventContext
		if e = json.Unmarshal(event.Context, &eventContext); e != nil {
			reject("INVALID_EVENT_CONTEXT", false, "context")
			out.Results[i] = result
			continue
		}
		records = append(records, Record{ProjectID: source.Source.ProjectID, SourceID: source.Source.ID, SourceKind: source.Source.Kind, Environment: source.Source.Environment, ReceivedAt: now, EventID: event.ID, EventKey: event.Key, SchemaVersion: event.Version, ReleaseRevision: event.Revision, OccurredAt: event.Occurred.UTC(), UID: event.Identity.UID, AnonymousID: event.Identity.AnonymousID, Context: eventContext, ContextJSON: event.Context, PropertiesJSON: event.Properties, SDKName: event.SDK.Name, SDKVersion: event.SDK.Version})
		result.Status = "accepted"
		out.Results[i] = result
	}
	if len(records) == 0 {
		if retryable {
			return Receipt{}, 0, unavailable("EVENT_PLAN_NOT_READY")
		}
		return out, 422, nil
	}
	if e = c.queue.Publish(ctx, records); e != nil {
		return Receipt{}, 0, unavailable("DURABLE_QUEUE_UNAVAILABLE")
	}
	return out, 202, nil
}
