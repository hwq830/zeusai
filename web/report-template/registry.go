package reporttemplate

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"sync"
)

// Template is one parsed page template version plus everything rendering needs. It is
// immutable once built, so callers may cache it per version id.
type Template struct {
	ID          string
	page        *template.Template
	chartScript string
	shellScript string
	csp         string
}

var templateFuncs = template.FuncMap{
	"isWeb":  func(u string) bool { return isWebURL(u) },
	"printf": fmt.Sprintf,
}

// NewTemplate parses a stored page template. Only the fixed function table is exposed:
// a stored template cannot reach files, the network or the environment.
func NewTemplate(id string, files map[string]string, cspHashes []string) (*Template, error) {
	text, ok := files[TemplateFile]
	if !ok || text == "" {
		return nil, fmt.Errorf("page template: %s is missing", TemplateFile)
	}
	page, err := template.New("report").Funcs(templateFuncs).Option("missingkey=error").Parse(text)
	if err != nil {
		return nil, fmt.Errorf("page template: %w", err)
	}
	hashes := cspHashes
	if len(hashes) == 0 {
		hashes = ComputeCSPHashes(text, files[ChartScriptFile])
	}
	return &Template{
		ID: id, page: page, chartScript: files[ChartScriptFile],
		shellScript: visualShellScript, csp: CSPFromHashes(hashes),
	}, nil
}

// Render executes this version with the same inputs the embedded template gets.
func (t *Template) Render(d Data) ([]byte, error) {
	return renderWith(t.page, d, t.chartScript, t.shellScript, t.csp)
}

// Provider supplies the active page template for new renders. Returning (nil, nil) means
// "no stored version", which falls back to the embedded template without an error.
type Provider interface {
	ReportTemplate(ctx context.Context) (*Template, error)
}

var registry struct {
	sync.RWMutex
	provider Provider
}

// SetProvider installs the stored-template provider; nil restores the embedded template.
func SetProvider(p Provider) {
	registry.Lock()
	defer registry.Unlock()
	registry.provider = p
}

func currentProvider() Provider {
	registry.RLock()
	defer registry.RUnlock()
	return registry.provider
}

// Render renders with the active stored version when one is available, and falls back to the
// embedded template when it is missing, unreadable or cannot execute. A render must never
// fail because the template store is down.
func Render(d Data) ([]byte, error) { return RenderContext(context.Background(), d) }

// RenderContext is Render with a caller context for the template lookup.
func RenderContext(ctx context.Context, d Data) ([]byte, error) {
	if p := currentProvider(); p != nil {
		tpl, err := p.ReportTemplate(ctx)
		if err != nil {
			slog.Warn("page template fallback", "kind", "report", "reason", err.Error())
		} else if tpl != nil {
			out, renderErr := tpl.Render(d)
			if renderErr == nil {
				return out, nil
			}
			slog.Warn("page template fallback", "kind", "report", "version", tpl.ID, "reason", renderErr.Error())
		}
	}
	return renderWith(page, d, visualChartScript, visualShellScript, visualCSP)
}

// RenderBuiltin renders with the embedded template regardless of any configured provider.
// The publish gate uses it as the known-good baseline for structural checks.
func RenderBuiltin(d Data) ([]byte, error) {
	return renderWith(page, d, visualChartScript, visualShellScript, visualCSP)
}

// BuiltinFiles returns the embedded page files, used to seed the first stored version.
func BuiltinFiles() map[string]string {
	return map[string]string{TemplateFile: src, ChartScriptFile: visualChartScript}
}

// ErrNoPageTemplate means no stored version exists for the kind.
var ErrNoPageTemplate = errors.New("page template: no active version")
