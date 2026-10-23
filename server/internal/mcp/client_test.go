package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeServer speaks just enough MCP; sse makes tools/call answer as an event stream.
func fakeServer(t *testing.T, sse bool) (*httptest.Server, *[]string) {
	var methods []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		methods = append(methods, req.Method)
		if req.Method != "initialize" && r.Header.Get("Mcp-Session-Id") != "s1" {
			http.Error(w, "no session", http.StatusBadRequest)
			return
		}
		if req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "s1")
			result = map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}}
		case "tools/list":
			var p struct{ Cursor string }
			_ = json.Unmarshal(req.Params, &p)
			if p.Cursor == "" {
				result = map[string]any{"tools": []Tool{{Name: "a"}}, "nextCursor": "2"}
			} else {
				result = map[string]any{"tools": []Tool{{Name: "b", InputSchema: json.RawMessage(`{"type":"object"}`)}}}
			}
		case "tools/call":
			var p struct {
				Name      string
				Arguments map[string]any
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.Name == "boom" {
				writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": *req.ID, "error": map[string]any{"code": -32602, "message": "unknown tool"}})
				return
			}
			result = map[string]any{"content": []Content{{Type: "text", Text: fmt.Sprintf("%s(%v)", p.Name, p.Arguments["q"])}}}
			if sse {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n")
				b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": result})
				fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
				return
			}
		}
		writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": result})
	}))
	t.Cleanup(s.Close)
	return s, &methods
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func TestClient(t *testing.T) {
	for _, sse := range []bool{false, true} {
		s, methods := fakeServer(t, sse)
		c := &Client{Endpoint: s.URL}
		tools, err := c.ListTools(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(tools) != 2 || tools[1].Name != "b" {
			t.Fatalf("tools %+v", tools)
		}
		res, err := c.CallTool(context.Background(), "a", map[string]any{"q": "上海"})
		if err != nil {
			t.Fatal(err)
		}
		if res.Text() != "a(上海)" {
			t.Fatalf("sse=%v result %+v", sse, res)
		}
		if _, err := c.CallTool(context.Background(), "boom", nil); err == nil {
			t.Fatal("rpc error not returned")
		}
		want := []string{"initialize", "notifications/initialized", "tools/list", "tools/list", "tools/call", "tools/call"}
		if fmt.Sprint(*methods) != fmt.Sprint(want) {
			t.Fatalf("methods %v", *methods)
		}
	}
}
