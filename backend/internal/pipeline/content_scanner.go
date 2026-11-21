package pipeline

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/howtomakeaname/Evia/backend/internal/content"
)

const contentScanResponseBound = 4 << 20

type ContentScanner struct {
	endpoint *url.URL
	database string
	username string
	password string
	client   *http.Client
}

func NewContentScanner(c ClickHouseConfig, client *http.Client) (*ContentScanner, error) {
	endpoint, err := url.Parse(c.URL)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" || endpoint.User != nil || !databasePattern.MatchString(c.Database) {
		return nil, errors.New("invalid clickhouse endpoint or database")
	}
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return &ContentScanner{endpoint: endpoint, database: c.Database, username: c.Username, password: c.Password, client: client}, nil
}

type encryptedRow struct {
	ProjectID      string `json:"project_id"`
	Environment    string `json:"environment"`
	EventID        string `json:"event_id"`
	ReceivedAtNS   int64  `json:"received_at_ns"`
	PropertiesJSON string `json:"properties_json"`
}

type encryptedProperties struct {
	ConversationID string `json:"conversation_id"`
	MessageID      string `json:"message_id"`
	Role           string `json:"role"`
	ChunkIndex     int    `json:"chunk_index"`
	ChunkCount     int    `json:"chunk_count"`
	Ciphertext     string `json:"ciphertext"`
	Nonce          string `json:"nonce"`
	WrappedKey     string `json:"wrapped_key"`
	KeyID          string `json:"key_id"`
	Algorithm      string `json:"alg"`
	AADVersion     string `json:"aad_version"`
	Padding        string `json:"padding"`
	ContentLength  int    `json:"content_length"`
	Truncated      bool   `json:"truncated"`
	ResponseStatus string `json:"response_status"`
	DurationMS     int    `json:"duration_ms"`
}

func (s *ContentScanner) ScanEncryptedContent(ctx context.Context, cursor content.Cursor, limit int) ([]content.EncryptedChunk, error) {
	if limit <= 0 || limit > content.MaxScanBatch {
		limit = content.MaxScanBatch
	}
	query := "SELECT project_id,environment,event_id,toUnixTimestamp64Nano(received_at) AS received_at_ns,properties_json" +
		" FROM `" + s.database + "`.event_facts WHERE event_key='chat_content_chunk'" +
		" AND tuple(received_at,event_id)>tuple(fromUnixTimestamp64Nano({after:Int64}),{event:String})" +
		" ORDER BY received_at,event_id LIMIT {limit:UInt64} FORMAT JSONEachRow"
	body, err := s.request(ctx, query, map[string]string{
		"after": strconv.FormatInt(cursor.ReceivedAt.UnixNano(), 10), "event": cursor.EventID, "limit": strconv.Itoa(limit),
	})
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 64*1024), contentScanResponseBound)
	chunks := make([]content.EncryptedChunk, 0, limit)
	for scanner.Scan() {
		var row encryptedRow
		if err = json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, fmt.Errorf("decode encrypted content row: %w", err)
		}
		var properties encryptedProperties
		if err = json.Unmarshal([]byte(row.PropertiesJSON), &properties); err != nil {
			return nil, fmt.Errorf("decode encrypted content properties: %w", err)
		}
		if properties.ChunkCount < 1 || properties.ChunkCount > 32 || properties.ChunkIndex < 0 || properties.ChunkIndex >= properties.ChunkCount || len(properties.Ciphertext) == 0 || len(properties.Ciphertext) > 1024 || len(properties.WrappedKey) == 0 || len(properties.WrappedKey) > 1024 {
			return nil, errors.New("encrypted content properties exceed bounds")
		}
		chunks = append(chunks, content.EncryptedChunk{
			ProjectID: row.ProjectID, Environment: row.Environment, EventID: row.EventID,
			ReceivedAt:     time.Unix(0, row.ReceivedAtNS).UTC(),
			ConversationID: properties.ConversationID, MessageID: properties.MessageID, Role: properties.Role,
			ChunkIndex: properties.ChunkIndex, ChunkCount: properties.ChunkCount,
			Ciphertext: properties.Ciphertext, Nonce: properties.Nonce, WrappedKey: properties.WrappedKey,
			KeyID: properties.KeyID, Algorithm: properties.Algorithm,
			AADVersion: properties.AADVersion, Padding: properties.Padding,
			ContentLength: properties.ContentLength, Truncated: properties.Truncated,
			ResponseStatus: properties.ResponseStatus, DurationMS: properties.DurationMS,
		})
	}
	return chunks, scanner.Err()
}

func (s *ContentScanner) request(ctx context.Context, query string, parameters map[string]string) ([]byte, error) {
	endpoint := *s.endpoint
	values := endpoint.Query()
	values.Set("database", s.database)
	values.Set("wait_end_of_query", "1")
	values.Set("output_format_json_quote_64bit_integers", "0")
	for key, value := range parameters {
		values.Set("param_"+key, value)
	}
	endpoint.RawQuery = values.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), strings.NewReader(query))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "text/plain; charset=utf-8")
	if s.username != "" || s.password != "" {
		request.SetBasicAuth(s.username, s.password)
	}
	response, err := s.client.Do(request)
	if err != nil {
		return nil, ErrSinkUnavailable
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, contentScanResponseBound+1))
	if err != nil || len(body) > contentScanResponseBound {
		return nil, ErrSinkUnavailable
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w (HTTP %d): %s", ErrSinkUnavailable, response.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}
