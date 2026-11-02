package search

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// StatusError is a non-200 answer from a search API; the console maps 401/403/429 to advice.
type StatusError struct {
	Provider string
	Status   int
	Body     string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s: HTTP %d: %s", e.Provider, e.Status, e.Body)
}

func do(hc *http.Client, req *http.Request, name string) ([]byte, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{Provider: name, Status: resp.StatusCode, Body: string(bytes.TrimSpace(raw[:min(len(raw), 300)]))}
	}
	return raw, nil
}

// parseTime returns the first value that parses as a time (UTC), or nil.
func parseTime(values ...string) *time.Time {
	for _, d := range values {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
			if t, err := time.Parse(layout, d); err == nil {
				t = t.UTC()
				return &t
			}
		}
	}
	return nil
}

func limit(n, def, max int) int {
	if n <= 0 || n > max {
		return def
	}
	return n
}
