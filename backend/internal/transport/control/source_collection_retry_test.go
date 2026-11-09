package control

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
)

type deniedCollectionBudget struct{ kind fault.Kind }

func (b deniedCollectionBudget) ReserveCollection(context.Context, string, string, int) error {
	return fault.New(b.kind, "COLLECTION_BUDGET_UNAVAILABLE", "接收预算暂不可用")
}

func TestCollectorHTTPBrowserCanReadRetryAdviceWithoutCredentials(t *testing.T) {
	for _, tc := range []struct {
		name   string
		kind   fault.Kind
		status int
	}{
		{"rate limited", fault.Limited, http.StatusTooManyRequests},
		{"budget unavailable", fault.Unavailable, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			queue := &collectionQueue{}
			api := collectionAdapter(t, queue)
			validator, err := contract.New()
			if err != nil {
				t.Fatal(err)
			}
			api.Collector, err = ingestion.NewCollector(collectionSources{}, collectionPlans{}, queue, deniedCollectionBudget{tc.kind}, validator)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/v1/collect", strings.NewReader(collectionBody(collectionEvent())))
			request.Header.Set("Origin", "https://website.example")
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			api.Handler().ServeHTTP(response, request)
			if response.Code != tc.status || queue.calls != 0 || strings.Contains(response.Body.String(), `"accepted"`) {
				t.Fatal("admission failure must not acknowledge an event", response.Code, queue.calls)
			}
			if response.Header().Get("Access-Control-Allow-Origin") != "https://website.example" || response.Header().Get("Access-Control-Allow-Credentials") != "" {
				t.Fatal("retry advice must not grant credentialed browser access")
			}
			exposed := map[string]bool{}
			for _, name := range strings.Split(response.Header().Get("Access-Control-Expose-Headers"), ",") {
				exposed[strings.ToLower(strings.TrimSpace(name))] = true
			}
			for _, name := range []string{"Retry-After", "X-Request-ID"} {
				if !exposed[strings.ToLower(name)] || response.Header().Get(name) == "" {
					t.Fatal("browser cannot inspect safe retry metadata", name)
				}
			}
		})
	}
}
