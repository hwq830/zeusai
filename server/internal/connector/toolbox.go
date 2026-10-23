package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/mcp"
)

// ToolSpec is one read-only tool a task may call for a user.
type ToolSpec struct {
	Connector     string          `json:"connector"`
	ConnectorName string          `json:"connectorName"`
	Tool          string          `json:"tool"`
	Description   string          `json:"description,omitempty"`
	InputSchema   json.RawMessage `json:"inputSchema,omitempty"`
}

// Toolbox exposes whitelisted remote MCP tools to workflows. Only tools that are both in
// catalog.yaml with write=false and advertised by the server are offered or callable; write
// tools go through the confirmation flow, which reports do not use.
type Toolbox struct {
	Q *db.Queries
	// Endpoint resolves a connector's server URL; defaults to its endpoint_env variable.
	Endpoint func(id string) string

	mu      sync.Mutex
	clients map[string]*mcp.Client
	cache   map[string]cachedTools
}

type cachedTools struct {
	tools []mcp.Tool
	at    time.Time
}

const toolsTTL = 10 * time.Minute

func (t *Toolbox) endpoint(e catalogEntry) string {
	if t.Endpoint != nil {
		return t.Endpoint(e.ID)
	}
	if e.EndpointEnv == "" {
		return ""
	}
	return os.Getenv(e.EndpointEnv)
}

func (t *Toolbox) client(id, url string) *mcp.Client {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.clients == nil {
		t.clients = map[string]*mcp.Client{}
		t.cache = map[string]cachedTools{}
	}
	c := t.clients[id]
	if c == nil || c.Endpoint != url {
		c = &mcp.Client{Endpoint: url}
		t.clients[id] = c
	}
	return c
}

func (t *Toolbox) serverTools(ctx context.Context, id string, c *mcp.Client) ([]mcp.Tool, error) {
	t.mu.Lock()
	ct, ok := t.cache[id]
	t.mu.Unlock()
	if ok && time.Since(ct.at) < toolsTTL {
		return ct.tools, nil
	}
	tools, err := c.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.cache[id] = cachedTools{tools: tools, at: time.Now()}
	t.mu.Unlock()
	return tools, nil
}

// Available lists the read-only tools the user has turned on. A connector whose server is
// unreachable is skipped rather than failing the task.
func (t *Toolbox) Available(ctx context.Context, userID uuid.UUID) ([]ToolSpec, error) {
	entries, err := catalog()
	if err != nil {
		return nil, err
	}
	rows, err := t.Q.ListConnectorsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	enabled := map[string]bool{}
	for _, r := range rows {
		// Credentialed connectors need the KMS-backed token path, which is not wired yet.
		enabled[r.ID] = r.Enabled && !r.RequiresAuth
	}
	var out []ToolSpec
	for _, e := range entries {
		url := t.endpoint(e)
		if e.Kind != "mcp" || e.Transport != "http" || !enabled[e.ID] || url == "" {
			continue
		}
		readOnly := map[string]Tool{}
		for _, tl := range e.Tools {
			if !tl.Write {
				readOnly[tl.Name] = tl
			}
		}
		served, err := t.serverTools(ctx, e.ID, t.client(e.ID, url))
		if err != nil {
			continue
		}
		for _, st := range served {
			cat, ok := readOnly[st.Name]
			if !ok {
				continue
			}
			desc := st.Description
			if desc == "" {
				desc = cat.Description
			}
			out = append(out, ToolSpec{Connector: e.ID, ConnectorName: e.Name, Tool: st.Name, Description: desc, InputSchema: st.InputSchema})
		}
	}
	return out, nil
}

// Call runs a tool after checking it is among the user's available read-only tools.
func (t *Toolbox) Call(ctx context.Context, userID uuid.UUID, connector, tool string, args map[string]any) (string, error) {
	specs, err := t.Available(ctx, userID)
	if err != nil {
		return "", err
	}
	for _, s := range specs {
		if s.Connector == connector && s.Tool == tool {
			t.mu.Lock()
			c := t.clients[connector]
			t.mu.Unlock()
			res, err := c.CallTool(ctx, tool, args)
			if err != nil {
				return "", err
			}
			if res.IsError {
				return "", fmt.Errorf("%s/%s: %s", connector, tool, res.Text())
			}
			return res.Text(), nil
		}
	}
	return "", fmt.Errorf("tool %s/%s is not available to this user", connector, tool)
}
