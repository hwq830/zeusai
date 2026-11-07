package pagetemplate

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/pdf"
	reporttemplate "github.com/hwq830/zeusai/web/report-template"
)

// VerifyTimeout bounds the whole external check (layout + PDF).
const VerifyTimeout = 30 * time.Second

// VerifyResult is the orchestrator's answer: a per-check outcome, not an error. Transport or
// configuration failures are reported as the error return instead.
type VerifyResult struct {
	Viewport struct {
		OK          bool   `json:"ok"`
		ScrollWidth int    `json:"scrollWidth"`
		InnerWidth  int    `json:"innerWidth"`
		Error       string `json:"error,omitempty"`
	} `json:"viewport"`
	PDF struct {
		OK    bool   `json:"ok"`
		Bytes int    `json:"bytes"`
		Error string `json:"error,omitempty"`
	} `json:"pdf"`
}

// Verifier checks a rendered page for horizontal overflow and printability.
type Verifier interface {
	Verify(ctx context.Context, html []byte) (VerifyResult, error)
}

// ChromiumVerifier runs both checks in this process (the orchestrator has Chromium).
type ChromiumVerifier struct {
	Chromium pdf.Chromium
}

func (v ChromiumVerifier) Verify(ctx context.Context, page []byte) (VerifyResult, error) {
	var out VerifyResult
	viewport, err := v.Chromium.MeasureViewport(ctx, page, 390)
	if err != nil {
		out.Viewport.Error = err.Error()
	} else {
		out.Viewport.ScrollWidth, out.Viewport.InnerWidth = viewport.ScrollWidth, viewport.InnerWidth
		out.Viewport.OK = viewport.ScrollWidth <= viewport.InnerWidth+1
	}
	printed, err := v.Chromium.Render(ctx, page)
	switch {
	case err != nil:
		out.PDF.Error = err.Error()
	case len(printed) == 0 || !bytes.HasPrefix(printed, []byte("%PDF")):
		out.PDF.Error = "打印结果不是 PDF"
	default:
		out.PDF.OK = true
		out.PDF.Bytes = len(printed)
	}
	return out, nil
}

// RemoteVerifier calls the orchestrator's internal verification endpoint.
type RemoteVerifier struct {
	URL    string
	Token  string
	Client *http.Client
}

func NewRemoteVerifier(url, token string) *RemoteVerifier {
	return &RemoteVerifier{URL: strings.TrimRight(url, "/"), Token: token, Client: &http.Client{}}
}

func (r *RemoteVerifier) Verify(ctx context.Context, page []byte) (VerifyResult, error) {
	var out VerifyResult
	ctx, cancel := context.WithTimeout(ctx, VerifyTimeout)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"html": string(page)})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.URL+"/internal/page-template-verify", bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.Token)
	resp, err := r.Client.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("verify endpoint status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("verify response: %w", err)
	}
	return out, nil
}

// VerifyHandler is the orchestrator's internal endpoint. It accepts either the rendered HTML
// or a stored version id (rendered against the fixed fixture) and returns VerifyResult.
func VerifyHandler(token string, q *db.Queries, verifier Verifier) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if verifier == nil {
			http.Error(w, "verification is not configured", http.StatusServiceUnavailable)
			return
		}
		var in struct {
			HTML      string `json:"html"`
			VersionID string `json:"versionId"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&in); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		page := []byte(in.HTML)
		if len(page) == 0 && in.VersionID != "" {
			rendered, err := renderVersionFixture(r.Context(), q, in.VersionID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			page = rendered
		}
		if len(page) == 0 {
			http.Error(w, "html or versionId is required", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), VerifyTimeout)
		defer cancel()
		result, err := verifier.Verify(ctx, page)
		if err != nil {
			slog.Warn("page template verify failed", "err", err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})
}

func renderVersionFixture(ctx context.Context, q *db.Queries, versionID string) ([]byte, error) {
	id, parseErr := uuid.Parse(versionID)
	if parseErr != nil {
		return nil, fmt.Errorf("version not found")
	}
	v, err := q.GetPageTemplateVersion(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("version not found")
	}
	files := decodeFiles(v.Files)
	tpl, err := reporttemplate.NewTemplate(v.ID.String(), files, decodeHashes(v.CspHashes))
	if err != nil {
		return nil, fmt.Errorf("template cannot be parsed: %w", err)
	}
	return tpl.Render(FixtureData())
}
