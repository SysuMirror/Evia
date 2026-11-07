// Package httpapi implements the uniform HTTP boundary shared by all services.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/gen/wire"
	"github.com/howtomakeaname/Evia/backend/internal/platform/contract"
	"github.com/howtomakeaname/Evia/backend/internal/platform/fault"
	"github.com/howtomakeaname/Evia/backend/internal/platform/ids"
)

type requestKey struct{}
type Boundary struct {
	Validator *contract.Validator
	Logger    *slog.Logger
}
type Endpoint func(http.ResponseWriter, *http.Request) error

type statusWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}
func (w *statusWriter) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func RequestID(ctx context.Context) string { id, _ := ctx.Value(requestKey{}).(string); return id }

func (b *Boundary) Handler(fn Endpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			if errors.Is(err, context.DeadlineExceeded) && r.Context().Err() != nil {
				err = fault.New(fault.Unavailable, "REQUEST_TIMEOUT", "请求处理超时，请稍后重试")
			}
			b.Error(w, r, err)
		}
	}
}

func (b *Boundary) Middleware(next http.Handler) http.Handler {
	// The context deadline cancels database and other cooperative dependencies.
	// Server WriteTimeout alone bounds socket writes, not handler execution.
	return b.middleware(next, 20*time.Second)
}

func (b *Boundary) middleware(next http.Handler, timeout time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bounded, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		response := &statusWriter{ResponseWriter: w}
		w = response
		id := ids.New()
		r = r.WithContext(context.WithValue(bounded, requestKey{}, id))
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		defer func() {
			if recovered := recover(); recovered != nil {
				b.Logger.Error("request panic", "request_id", id)
				if response.wroteHeader {
					panic(http.ErrAbortHandler)
				}
				b.Error(w, r, fault.New(fault.Internal, "INTERNAL_ERROR", "请求未能完成"))
			}
		}()
		if _, err := url.ParseQuery(r.URL.RawQuery); err != nil {
			b.Error(w, r, fault.New(fault.Invalid, "INVALID_QUERY", "查询参数编码不合法"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (b *Boundary) Decode(w http.ResponseWriter, r *http.Request, schema string, target any) error {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return fault.New(fault.Invalid, "INVALID_CONTENT_TYPE", "请求必须使用 application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256*1024)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return fault.New(fault.Invalid, "BODY_TOO_LARGE", "请求正文超过限制")
		}
		return fault.New(fault.Invalid, "INVALID_JSON", "无法读取请求正文")
	}
	value, err := contract.Decode(raw)
	if err != nil {
		return fault.New(fault.Invalid, "INVALID_JSON", "请求正文不是有效的单一 JSON 值")
	}
	if err := b.Validator.Validate(schema, value); err != nil {
		return fault.New(fault.Invalid, "VALIDATION_FAILED", "请求字段不符合接口定义")
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.UseNumber()
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return fault.New(fault.Invalid, "VALIDATION_FAILED", "请求字段类型不正确")
	}
	return nil
}

func (b *Boundary) JSON(w http.ResponseWriter, status int, schema string, value any) error {
	raw, err := b.Validator.Encode(schema, value)
	if err != nil {
		b.Logger.Error("response contract violation", "schema", schema)
		return fault.New(fault.Internal, "RESPONSE_CONTRACT_FAILED", "响应生成失败")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, err = w.Write(append(raw, '\n'))
	// Headers have been committed: do not attempt a second error response.
	if err != nil {
		b.Logger.Warn("response write failed", "schema", schema)
	}
	return nil
}

func (b *Boundary) Error(w http.ResponseWriter, r *http.Request, err error) {
	f := fault.Public(err)
	statuses := map[fault.Kind]int{fault.Invalid: 400, fault.Unauthenticated: 401, fault.Forbidden: 403, fault.NotFound: 404, fault.Conflict: 409, fault.Unavailable: 503, fault.Limited: 429, fault.Internal: 500}
	status := statuses[f.Kind]
	if status == 0 {
		status = http.StatusInternalServerError
	}
	if f.Code == "BODY_TOO_LARGE" {
		status = 413
	}
	if f.Code == "INVALID_CONTENT_TYPE" {
		status = 415
	}
	if f.Code == "METHOD_NOT_ALLOWED" {
		status = 405
	}
	if f.Kind == fault.Limited || f.Kind == fault.Unavailable {
		w.Header().Set("Retry-After", "5")
	}
	if status >= 500 {
		b.Logger.Error("request failed", "request_id", RequestID(r.Context()), "code", f.Code)
	}
	problem := wire.Problem{Code: f.Code, Message: f.Message, RequestID: RequestID(r.Context())}
	if len(f.Fields) > 0 {
		problem.Details = map[string]any{"field_paths": f.Fields}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem)
}

func IfMatch(r *http.Request) (int64, error) {
	value := r.Header.Get("If-Match")
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' {
		return 0, fault.New(fault.Invalid, "REVISION_REQUIRED", "请提供带双引号的 If-Match 版本")
	}
	n, err := strconv.ParseInt(value[1:len(value)-1], 10, 64)
	if err != nil || n < 1 || n > 9007199254740991 {
		return 0, fault.New(fault.Invalid, "INVALID_REVISION", "版本必须为正整数")
	}
	return n, nil
}

func Limit(r *http.Request) (int, error) {
	if len(r.URL.Query()["limit"]) > 1 {
		return 0, fault.New(fault.Invalid, "INVALID_LIMIT", "limit 不可重复")
	}
	v := r.URL.Query().Get("limit")
	if v == "" {
		return 50, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 200 {
		return 0, fault.New(fault.Invalid, "INVALID_LIMIT", "limit 必须在 1 到 200 之间")
	}
	return n, nil
}

func Server(addr string, handler http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
}
