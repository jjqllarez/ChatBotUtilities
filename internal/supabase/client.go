package supabase

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is a thin REST client for Supabase (PostgREST) using the service role key.
type Client struct {
	baseURL string
	key     string
	http    *http.Client
}

func NewClient(supabaseURL, serviceKey string) *Client {
	return &Client{
		baseURL: strings.TrimRight(supabaseURL, "/") + "/rest/v1",
		key:     serviceKey,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *Client) do(ctx context.Context, method, table, query string, body any) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(b)
	}

	u := c.baseURL + "/" + table + query
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("apikey", c.key)
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode >= 300 {
		return data, resp.StatusCode, fmt.Errorf("supabase %s %s -> %d: %s", method, table, resp.StatusCode, truncate(string(data)))
	}
	return data, resp.StatusCode, nil
}

func truncate(s string) string {
	if len(s) > 300 {
		return s[:300]
	}
	return s
}

// Select runs a GET returning an array of JSON rows.
func (c *Client) Select(ctx context.Context, table, query string) ([]map[string]any, error) {
	data, _, err := c.do(ctx, http.MethodGet, table, query, nil)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	var rows []map[string]any
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// SelectOne runs a GET returning at most one row.
func (c *Client) SelectOne(ctx context.Context, table, query string) (map[string]any, bool, error) {
	rows, err := c.Select(ctx, table, query)
	if err != nil {
		return nil, false, err
	}
	if len(rows) == 0 {
		return nil, false, nil
	}
	return rows[0], true, nil
}

// normalizeBytes converts []byte values in a row to PostgreSQL's hex bytea
// format ("\\x<hex>"), since this PostgREST instance stores bytea columns as
// raw text rather than decoding base64.
func normalizeBytes(row map[string]any) map[string]any {
	for k, v := range row {
		if b, ok := v.([]byte); ok {
			row[k] = "\\x" + hex.EncodeToString(b)
		}
	}
	return row
}

// Upsert inserts the row, updating on conflict for the given columns (merge-duplicates).
func (c *Client) Upsert(ctx context.Context, table string, row map[string]any, conflict []string) error {
	query := ""
	if len(conflict) > 0 {
		query = "?on_conflict=" + url.QueryEscape(strings.Join(conflict, ","))
	}
	u := c.baseURL + "/" + table + query
	b, err := json.Marshal(normalizeBytes(row))
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("apikey", c.key)
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Prefer", "resolution=merge-duplicates")
	return c.send(req)
}

// Insert inserts a new row (POST).
func (c *Client) Insert(ctx context.Context, table string, row map[string]any) error {
	b, err := json.Marshal(normalizeBytes(row))
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/"+table, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("apikey", c.key)
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Prefer", "return=minimal")
	return c.send(req)
}

// InsertIgnore inserts the row without erroring on conflicts.
func (c *Client) InsertIgnore(ctx context.Context, table string, row map[string]any) error {
	b, err := json.Marshal(normalizeBytes(row))
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/"+table, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("apikey", c.key)
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Prefer", "resolution=ignore-duplicates")
	return c.send(req)
}

// Update patches matching rows.
func (c *Client) Update(ctx context.Context, table, filter string, row map[string]any) error {
	b, err := json.Marshal(normalizeBytes(row))
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, c.baseURL+"/"+table+filter, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("apikey", c.key)
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Prefer", "return=minimal")
	return c.send(req)
}

// Delete removes rows matching the filter.
func (c *Client) Delete(ctx context.Context, table, filter string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/"+table+filter, nil)
	if err != nil {
		return err
	}
	req.Header.Set("apikey", c.key)
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Prefer", "return=minimal")
	return c.send(req)
}

func (c *Client) send(req *http.Request) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("supabase %s -> %d: %s", req.URL.Path, resp.StatusCode, truncate(string(data)))
	}
	return nil
}

// RPC calls a Supabase PostgREST function (POST /rest/v1/rpc/<name>) and
// decodes the JSON response into dest (pass *json.RawMessage to inspect raw).
func (c *Client) RPC(ctx context.Context, name string, args map[string]any, dest any) error {
	b, err := json.Marshal(args)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/rpc/"+name, bytes.NewReader(b))
	if err != nil {
		return err
	}
	return c.exec(req, dest)
}

// EdgeFunction calls a Supabase Edge Function (POST /functions/v1/<name>).
func (c *Client) EdgeFunction(ctx context.Context, name string, args any, dest any) error {
	b, err := json.Marshal(args)
	if err != nil {
		return err
	}
	site := strings.TrimSuffix(c.baseURL, "/rest/v1")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, site+"/functions/v1/"+name, bytes.NewReader(b))
	if err != nil {
		return err
	}
	return c.exec(req, dest)
}

func (c *Client) exec(req *http.Request, dest any) error {
	req.Header.Set("apikey", c.key)
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("supabase %s -> %d: %s", req.URL.Path, resp.StatusCode, truncate(string(data)))
	}
	if dest != nil && len(data) > 0 {
		if err := json.Unmarshal(data, dest); err != nil {
			return err
		}
	}
	return nil
}

// Row helpers to decode PostgREST JSON rows.
func GetBytes(m map[string]any, key string) []byte {
	if v, ok := m[key]; ok && v != nil {
		if s, ok2 := v.(string); ok2 {
			if strings.HasPrefix(s, `\x`) {
				if b, err := hex.DecodeString(strings.TrimPrefix(s, `\x`)); err == nil {
					return b
				}
			}
			if b, err := base64.StdEncoding.DecodeString(s); err == nil {
				return b
			}
			return []byte(s)
		}
	}
	return nil
}

func GetString(m map[string]any, key string) string {
	if v, ok := m[key]; ok && v != nil {
		if s, ok2 := v.(string); ok2 {
			return s
		}
	}
	return ""
}

func GetInt(m map[string]any, key string) int64 {
	if v, ok := m[key]; ok && v != nil {
		switch n := v.(type) {
		case float64:
			return int64(n)
		case json.Number:
			if i, err := n.Int64(); err == nil {
				return i
			}
		}
	}
	return 0
}

func GetBool(m map[string]any, key string) bool {
	if v, ok := m[key]; ok && v != nil {
		if b, ok2 := v.(bool); ok2 {
			return b
		}
	}
	return false
}

func GetFloat(m map[string]any, key string) float64 {
	if v, ok := m[key]; ok && v != nil {
		switch n := v.(type) {
		case float64:
			return n
		case json.Number:
			if f, err := n.Float64(); err == nil {
				return f
			}
		}
	}
	return 0
}

// kv builds a PostgREST equality filter fragment from a column/value pair.
func kv(column, value string) string {
	return column + "=eq." + url.QueryEscape(value)
}
