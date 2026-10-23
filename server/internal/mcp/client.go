// Package mcp is a minimal Model Context Protocol client over the Streamable HTTP transport:
// initialize, tools/list and tools/call. It only talks to whitelisted remote servers; stdio
// servers run inside the per-task sandbox and are reached through sandboxd instead.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const protocolVersion = "2025-06-18"

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

type Content struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type CallResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

// Text joins the result's text parts.
func (r CallResult) Text() string {
	var parts []string
	for _, c := range r.Content {
		if c.Type == "text" && c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// RPCError is a JSON-RPC error returned by the server.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("mcp: %d %s", e.Code, e.Message) }

type Client struct {
	Endpoint string
	// Header is added to every request, e.g. Authorization for a user's OAuth token.
	Header http.Header
	HTTP   *http.Client

	mu      sync.Mutex
	session string
	ready   bool
	nextID  atomic.Int64
}

type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int64 `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type response struct {
	ID     *int64          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *RPCError       `json:"error"`
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// post sends one JSON-RPC message. For requests it returns the matching response's result,
// whether the server answered with JSON or with an SSE stream.
func (c *Client) post(ctx context.Context, method string, params any, notify bool) (json.RawMessage, error) {
	msg := request{JSONRPC: "2.0", Method: method, Params: params}
	var id int64
	if !notify {
		id = c.nextID.Add(1)
		msg.ID = &id
	}
	body, _ := json.Marshal(msg)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range c.Header {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	c.mu.Lock()
	if c.session != "" {
		req.Header.Set("Mcp-Session-Id", c.session)
	}
	c.mu.Unlock()
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
		c.mu.Lock()
		c.session = s
		c.mu.Unlock()
	}
	if resp.StatusCode == http.StatusNotFound && c.session != "" {
		// The server dropped our session; start over on the next call.
		c.mu.Lock()
		c.session, c.ready = "", false
		c.mu.Unlock()
	}
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("mcp %s: HTTP %d: %s", method, resp.StatusCode, bytes.TrimSpace(raw))
	}
	if notify {
		return nil, nil
	}
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	body2 := io.LimitReader(resp.Body, 8<<20)
	if ct == "text/event-stream" {
		return readSSE(body2, id)
	}
	var r response
	if err := json.NewDecoder(body2).Decode(&r); err != nil {
		return nil, fmt.Errorf("mcp %s: %w", method, err)
	}
	return r.result()
}

func (r response) result() (json.RawMessage, error) {
	if r.Error != nil {
		return nil, r.Error
	}
	return r.Result, nil
}

func readSSE(r io.Reader, id int64) (json.RawMessage, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	var data strings.Builder
	flush := func() (json.RawMessage, bool, error) {
		defer data.Reset()
		if data.Len() == 0 {
			return nil, false, nil
		}
		var resp response
		if err := json.Unmarshal([]byte(data.String()), &resp); err != nil || resp.ID == nil || *resp.ID != id {
			return nil, false, nil // a notification or a server request: not ours
		}
		res, err := resp.result()
		return res, true, err
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if res, ok, err := flush(); ok {
				return res, err
			}
			continue
		}
		if v, ok := strings.CutPrefix(line, "data:"); ok {
			data.WriteString(strings.TrimPrefix(v, " "))
		}
	}
	if res, ok, err := flush(); ok {
		return res, err
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("mcp: stream ended without a response")
}

func (c *Client) ensureInit(ctx context.Context) error {
	c.mu.Lock()
	ready := c.ready
	c.mu.Unlock()
	if ready {
		return nil
	}
	_, err := c.post(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "zeus", "version": "0.1"},
	}, false)
	if err != nil {
		return err
	}
	if _, err := c.post(ctx, "notifications/initialized", nil, true); err != nil {
		return err
	}
	c.mu.Lock()
	c.ready = true
	c.mu.Unlock()
	return nil
}

func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	if err := c.ensureInit(ctx); err != nil {
		return nil, err
	}
	var all []Tool
	cursor := ""
	for range 10 {
		var params any
		if cursor != "" {
			params = map[string]string{"cursor": cursor}
		}
		raw, err := c.post(ctx, "tools/list", params, false)
		if err != nil {
			return nil, err
		}
		var page struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Tools...)
		if cursor = page.NextCursor; cursor == "" {
			break
		}
	}
	return all, nil
}

func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (CallResult, error) {
	if err := c.ensureInit(ctx); err != nil {
		return CallResult{}, err
	}
	if args == nil {
		args = map[string]any{}
	}
	raw, err := c.post(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, false)
	if err != nil {
		return CallResult{}, err
	}
	var r CallResult
	return r, json.Unmarshal(raw, &r)
}
