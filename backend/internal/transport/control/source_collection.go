package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/ingestion"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/httpapi"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type CollectionAPI struct {
	Boundary  *httpapi.Boundary
	Collector *ingestion.Collector
	header    *jsonschema.Schema
	timeout   time.Duration
}

func NewCollectionAPI(boundary *httpapi.Boundary, collector *ingestion.Collector) (*CollectionAPI, error) {
	// Preserve the authoritative batch schema while validating event envelopes
	// individually so one permanently invalid event cannot poison its siblings.
	var bundle map[string]any
	if e := json.Unmarshal(wire.Schemas, &bundle); e != nil {
		return nil, e
	}
	defs := bundle["$defs"].(map[string]any)
	batch := defs["CollectRequest"].(map[string]any)
	props := batch["properties"].(map[string]any)
	props["events"].(map[string]any)["items"] = map[string]any{}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	const uri = "https://schemas.evia.invalid/contracts/v1"
	if e := compiler.AddResource(uri, bundle); e != nil {
		return nil, e
	}
	header, e := compiler.Compile(uri + "#/$defs/CollectRequest")
	if e != nil {
		return nil, e
	}
	return &CollectionAPI{Boundary: boundary, Collector: collector, header: header, timeout: 5 * time.Second}, nil
}
func (a *CollectionAPI) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/collect", a.Boundary.Handler(a.collect))
	mux.HandleFunc("OPTIONS /v1/collect", a.Boundary.Handler(a.options))
	mux.HandleFunc("/", a.Boundary.Handler(func(w http.ResponseWriter, r *http.Request) error {
		if r.URL.Path == "/v1/collect" {
			w.Header().Set("Allow", "POST, OPTIONS")
			return fault.New(fault.Invalid, "METHOD_NOT_ALLOWED", "请求方法不被允许")
		}
		return fault.New(fault.NotFound, "ENDPOINT_NOT_FOUND", "接口不存在")
	}))
	return a.Boundary.Middleware(mux)
}
func collectionCORS(w http.ResponseWriter, origin string) {
	if origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Expose-Headers", "Retry-After, X-Request-ID")
		w.Header().Add("Vary", "Origin")
	}
}
func (a *CollectionAPI) options(w http.ResponseWriter, r *http.Request) error {
	origin := r.Header.Get("Origin")
	if _, e := ingestion.NormalizeOrigins([]string{origin}); e != nil {
		return e
	}
	if r.Header.Get("Access-Control-Request-Method") != "POST" {
		return ingestion.Invalid("INVALID_PREFLIGHT", "预检仅支持 POST")
	}
	for _, header := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
		h := strings.ToLower(strings.TrimSpace(header))
		if h != "" && h != "content-type" {
			return fault.New(fault.Forbidden, "COLLECTION_HEADER_FORBIDDEN", "浏览器接收端不允许此请求头")
		}
	}
	collectionCORS(w, origin)
	w.Header().Set("Access-Control-Allow-Methods", "POST")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Access-Control-Max-Age", "300")
	w.WriteHeader(http.StatusNoContent)
	return nil
}
func (a *CollectionAPI) collect(w http.ResponseWriter, r *http.Request) error {
	if origin := r.Header.Get("Origin"); origin != "" {
		if _, e := ingestion.NormalizeOrigins([]string{origin}); e != nil {
			return e
		}
		collectionCORS(w, origin)
	}
	if r.URL.RawQuery != "" {
		return ingestion.Invalid("BAD_BATCH", "接收端不接受查询参数")
	}
	media, params, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || (media != "application/json" && media != "text/plain") || (params["charset"] != "" && !strings.EqualFold(params["charset"], "utf-8")) {
		return fault.New(fault.Invalid, "INVALID_CONTENT_TYPE", "接收端仅支持 UTF-8 JSON 或 text/plain")
	}
	if len(r.Header.Values("Authorization")) > 1 || len(r.Header.Values("Origin")) > 1 {
		return ingestion.Invalid("BAD_BATCH", "接收请求头不可重复")
	}
	// Collection never authenticates with platform/website cookies. Rejecting
	// them makes an accidental credentialed browser configuration visible.
	if r.Header.Get("Cookie") != "" {
		return fault.New(fault.Forbidden, "COLLECTION_COOKIES_FORBIDDEN", "接收端不得携带网站或平台会话")
	}
	raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 256*1024))
	if e != nil {
		var large *http.MaxBytesError
		if errors.As(e, &large) {
			return fault.New(fault.Invalid, "BODY_TOO_LARGE", "批次正文超过限制")
		}
		return ingestion.Invalid("BAD_BATCH", "无法读取批次")
	}
	value, e := contract.Decode(raw)
	if e != nil || a.header.Validate(value) != nil {
		return ingestion.Invalid("BAD_BATCH", "批次结构不符合接口定义")
	}
	var in struct {
		ID       string            `json:"batch_id"`
		WriteKey string            `json:"write_key"`
		SentAt   time.Time         `json:"sent_at"`
		Events   []json.RawMessage `json:"events"`
	}
	if e = json.Unmarshal(raw, &in); e != nil {
		return ingestion.Invalid("BAD_BATCH", "批次字段无效")
	}
	bearer := ""
	if auth := r.Header.Get("Authorization"); auth != "" {
		parts := strings.Fields(auth)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			return fault.New(fault.Unauthenticated, "SOURCE_CREDENTIAL_INVALID", "来源凭据无效")
		}
		bearer = parts[1]
	}
	bounded, cancel := context.WithTimeout(r.Context(), a.timeout)
	defer cancel()
	r = r.WithContext(bounded)
	out, status, e := a.Collector.Collect(r.Context(), ingestion.Batch{ID: in.ID, WriteKey: in.WriteKey, SentAt: in.SentAt, Events: in.Events}, ingestion.Authentication{Bearer: bearer, Origin: r.Header.Get("Origin")})
	if e != nil {
		return e
	}
	collectionCORS(w, r.Header.Get("Origin"))
	return a.Boundary.JSON(w, status, "CollectResponse", out)
}
