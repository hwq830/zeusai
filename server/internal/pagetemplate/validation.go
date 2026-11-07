package pagetemplate

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"time"

	reporttemplate "github.com/hwq830/zeusai/web/report-template"
)

// Check statuses.
const (
	CheckPassed  = "passed"
	CheckFailed  = "failed"
	CheckSkipped = "skipped"
	CheckWarning = "warning"
)

// Check is one gate result.
type Check struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// Validation is the stored gate report.
type Validation struct {
	Passed     bool      `json:"passed"`
	Checks     []Check   `json:"checks"`
	DurationMS int64     `json:"durationMs"`
	Bytes      int       `json:"bytes"`
	RenderedAt time.Time `json:"renderedAt"`
}

var (
	scriptTagRe   = regexp.MustCompile(`(?s)<script([^>]*)>(.*?)</script>`)
	externalResRe = regexp.MustCompile(`(?i)(<script[^>]+src\s*=|<link[^>]+href\s*=|@import|url\s*\(\s*['"]?https?:|<img[^>]+src\s*=\s*['"]https?:|javascript:)`)
	sectionRe     = regexp.MustCompile(`<section id=`)
	figcaptionRe  = regexp.MustCompile(`<figcaption`)
	chartFigureRe = regexp.MustCompile(`<div class="fig-t">`)
	svgRe         = regexp.MustCompile(`<svg`)
)

// FixtureData is the fixed report every candidate template must render before publishing.
// Two charts, a table, citations and sources exercise the parts most likely to break.
func FixtureData() reporttemplate.Data {
	chart := func(anchor, title string) string {
		return "```zeus-chart\n" +
			`{"anchor":"` + anchor + `","type":"bar","title":"` + title + `","unit":"万人","level":"L1",` +
			`"series":[{"name":"基准","points":[{"label":"2024","value":100},{"label":"2025","value":200}]}],` +
			`"metric_definition":"测试指标"}` + "\n```\n\n"
	}
	markdown := "开篇说明，引用来源 [1]。\n\n## 一、市场规模\n\n" + chart("一、市场规模", "规模走势") +
		"| 年份 | 数值 |\n| --- | --- |\n| 2024 | 100 |\n| 2025 | 200 |\n\n## 二、趋势判断\n\n" +
		chart("二、趋势判断", "趋势对比") + "结论：需求继续增长 [2]。\n"
	return reporttemplate.Data{
		Title: "页面模板校验样例", Summary: "用于发布闸门的固定报告样例。", DepthLabel: "标准报告",
		SourceCount: 2, ReadMinutes: 6, CompletedAt: "2026-11-07", Lang: "zh",
		Sources: []reporttemplate.Source{
			{Index: 1, Title: "来源一", URL: "https://example.test/a", SiteName: "示例"},
			{Index: 2, Title: "来源二", URL: "https://example.test/b", SiteName: "示例"},
		},
		KeyFigures: []reporttemplate.KeyFigure{{Label: "规模", Value: "200", Unit: "万人", Cite: 1}},
		Markdown:   markdown,
	}
}

// RunGate executes the eight publish checks. A skipped check (no headless browser available)
// is not a failure; any failed check blocks publishing.
func RunGate(ctx context.Context, kind string, files map[string]string, hashes []string, verifier Verifier, verifyRequired bool) (Validation, []string) {
	result := Validation{RenderedAt: time.Now().UTC()}
	checks := make([]Check, 0, 8)
	add := func(id, name, status, detail string) {
		checks = append(checks, Check{ID: id, Name: name, Status: status, Detail: detail})
	}

	if hashes == nil {
		hashes = reporttemplate.ComputeCSPHashes(files[reporttemplate.TemplateFile], files[reporttemplate.ChartScriptFile])
	}
	tpl, err := reporttemplate.NewTemplate("validate", files, hashes)
	if err != nil {
		add("render", "渲染成功", CheckFailed, "模板无法解析："+err.Error())
		result.Checks = checks
		return result, hashes
	}
	fixture := FixtureData()
	expectedCharts := strings.Count(fixture.Markdown, "```zeus-chart")
	started := time.Now()
	out, renderErr := renderWithTimeout(tpl, fixture, 5*time.Second)
	result.DurationMS = time.Since(started).Milliseconds()
	result.Bytes = len(out)
	baseline, _ := reporttemplate.RenderBuiltin(fixture)
	htmlOut := string(out)

	if renderErr != nil {
		add("render", "渲染成功", CheckFailed, renderErr.Error())
	} else {
		add("render", "渲染成功", CheckPassed, fmt.Sprintf("%d 字节，%d ms", len(out), result.DurationMS))
	}

	figures := len(chartFigureRe.FindAll(out, -1))
	svgs := len(svgRe.FindAll(out, -1))
	if renderErr != nil {
		add("charts", "图表数量一致", CheckFailed, "渲染失败，无法核对图表")
	} else if figures != expectedCharts {
		add("charts", "图表数量一致", CheckFailed, fmt.Sprintf("样例有 %d 张图，渲染出 %d 个图表块", expectedCharts, figures))
	} else if svgs > 0 && svgs != expectedCharts {
		add("charts", "图表数量一致", CheckFailed, fmt.Sprintf("样例有 %d 张图，渲染出 %d 个 svg", expectedCharts, svgs))
	} else {
		add("charts", "图表数量一致", CheckPassed, fmt.Sprintf("%d 个图表块，%d 个 svg", figures, svgs))
	}

	if renderErr != nil {
		add("csp", "CSP 覆盖内联脚本", CheckFailed, "渲染失败，无法核对 CSP")
	} else if missing := uncoveredScripts(htmlOut, hashes); len(missing) > 0 {
		add("csp", "CSP 覆盖内联脚本", CheckFailed, "有内联脚本没有哈希："+strings.Join(missing, ", "))
	} else {
		add("csp", "CSP 覆盖内联脚本", CheckPassed, "全部内联脚本已授权")
	}

	if renderErr != nil {
		add("external", "无外链资源", CheckFailed, "渲染失败，无法核对外链")
	} else if match := externalResRe.FindString(htmlOut); match != "" {
		add("external", "无外链资源", CheckFailed, "发现外链或 javascript: "+match)
	} else {
		add("external", "无外链资源", CheckPassed, "未发现外部资源")
	}

	viewportCheck, pdfCheck := verifyPage(ctx, out, renderErr, verifier, verifyRequired)
	add("viewport", viewportCheck.Name, viewportCheck.Status, viewportCheck.Detail)

	if result.DurationMS >= 2000 {
		add("budget", "渲染耗时与体积", CheckFailed, fmt.Sprintf("%d ms，超过 2s", result.DurationMS))
	} else if result.Bytes > 2<<20 {
		add("budget", "渲染耗时与体积", CheckFailed, fmt.Sprintf("%d 字节，超过 2MB", result.Bytes))
	} else if renderErr == nil {
		add("budget", "渲染耗时与体积", CheckPassed, fmt.Sprintf("%d ms，%d 字节", result.DurationMS, result.Bytes))
	} else {
		add("budget", "渲染耗时与体积", CheckFailed, "渲染失败")
	}

	add("pdf", pdfCheck.Name, pdfCheck.Status, pdfCheck.Detail)

	// Structure is a warning: a new layout may legitimately change these counts, but a large
	// drift is worth showing next to the version.
	if renderErr != nil {
		add("structure", "结构自检", CheckFailed, "渲染失败，无法做结构自检")
	} else if detail := structureDrift(out, baseline); detail != "" {
		add("structure", "结构自检", CheckWarning, detail)
	} else {
		add("structure", "结构自检", CheckPassed, "章节、图注与图表数量与内置模板一致")
	}

	result.Passed = true
	for _, c := range checks {
		if c.Status == CheckFailed {
			result.Passed = false
		}
	}
	result.Checks = checks
	return result, hashes
}

func renderWithTimeout(tpl *reporttemplate.Template, data reporttemplate.Data, timeout time.Duration) ([]byte, error) {
	type outcome struct {
		out []byte
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		out, err := tpl.Render(data)
		done <- outcome{out, err}
	}()
	select {
	case r := <-done:
		return r.out, r.err
	case <-time.After(timeout):
		return nil, fmt.Errorf("渲染超过 %s", timeout)
	}
}

// verifyPage asks the orchestrator (or an injected verifier) for the layout and print
// checks. Fail closed by default: no verifier or an unreachable one blocks publishing unless
// ZEUS_PAGE_TEMPLATE_VERIFY_REQUIRED=false was set explicitly.
func verifyPage(ctx context.Context, page []byte, renderErr error, verifier Verifier, required bool) (Check, Check) {
	viewport := Check{ID: "viewport", Name: "390px 无横向溢出"}
	printed := Check{ID: "pdf", Name: "PDF 渲染"}
	if renderErr != nil {
		viewport.Status, viewport.Detail = CheckFailed, "网页渲染失败，无法校验视口"
		printed.Status, printed.Detail = CheckFailed, "网页渲染失败，无法打印"
		return viewport, printed
	}
	skipOrFail := func(detail string) {
		if required {
			viewport.Status, viewport.Detail = CheckFailed, detail+"（ZEUS_PAGE_TEMPLATE_VERIFY_REQUIRED=true，已阻断发布）"
			printed.Status, printed.Detail = CheckFailed, detail+"（ZEUS_PAGE_TEMPLATE_VERIFY_REQUIRED=true，已阻断发布）"
			return
		}
		viewport.Status, viewport.Detail = CheckSkipped, detail+"（ZEUS_PAGE_TEMPLATE_VERIFY_REQUIRED=false，跳过）"
		printed.Status, printed.Detail = CheckSkipped, detail+"（ZEUS_PAGE_TEMPLATE_VERIFY_REQUIRED=false，跳过）"
	}
	if verifier == nil {
		skipOrFail("未配置页面校验服务（ZEUS_PAGE_TEMPLATE_VERIFY_URL）")
		return viewport, printed
	}
	verifyCtx, cancel := context.WithTimeout(ctx, VerifyTimeout)
	defer cancel()
	result, err := verifier.Verify(verifyCtx, page)
	if err != nil {
		skipOrFail("页面校验服务不可达：" + err.Error())
		return viewport, printed
	}
	if result.Viewport.OK {
		viewport.Status, viewport.Detail = CheckPassed, fmt.Sprintf("scrollWidth %d ≤ innerWidth %d（390px 视口）", result.Viewport.ScrollWidth, result.Viewport.InnerWidth)
	} else {
		viewport.Status = CheckFailed
		viewport.Detail = fmt.Sprintf("390px 视口横向溢出：scrollWidth %d > innerWidth %d", result.Viewport.ScrollWidth, result.Viewport.InnerWidth)
		if result.Viewport.Error != "" {
			viewport.Detail += "；" + result.Viewport.Error
		}
	}
	if result.PDF.OK && result.PDF.Bytes > 0 {
		printed.Status, printed.Detail = CheckPassed, fmt.Sprintf("PDF %d 字节", result.PDF.Bytes)
	} else {
		printed.Status = CheckFailed
		printed.Detail = "PDF 渲染失败"
		if result.PDF.Error != "" {
			printed.Detail += "：" + result.PDF.Error
		}
	}
	return viewport, printed
}

// uncoveredScripts returns executable inline scripts whose hash is not allowed by the CSP.
func uncoveredScripts(html string, hashes []string) []string {
	allowed := map[string]bool{}
	for _, h := range hashes {
		allowed["sha256-"+h] = true
		allowed["sha256-"+strings.TrimPrefix(h, "sha256-")] = true
	}
	var missing []string
	for _, match := range scriptTagRe.FindAllStringSubmatch(html, -1) {
		attrs, body := match[1], match[2]
		if strings.Contains(attrs, "src=") {
			continue
		}
		if idx := strings.Index(attrs, "type="); idx >= 0 {
			typ := attrs[idx:]
			if !strings.Contains(typ, "javascript") && !strings.Contains(typ, "module") {
				continue // application/json data blocks are not executed
			}
		}
		sum := sha256.Sum256([]byte(body))
		hash := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
		if !allowed[hash] {
			missing = append(missing, hash)
		}
	}
	return missing
}

func structureDrift(candidate, baseline []byte) string {
	if len(baseline) == 0 {
		return ""
	}
	counts := func(b []byte) (sections, captions, svgs int) {
		return len(sectionRe.FindAll(b, -1)), len(figcaptionRe.FindAll(b, -1)), len(chartFigureRe.FindAll(b, -1))
	}
	cs, cc, cv := counts(candidate)
	bs, bc, bv := counts(baseline)
	var drift []string
	if cs != bs {
		drift = append(drift, fmt.Sprintf("章节 %d（内置 %d）", cs, bs))
	}
	if cc != bc {
		drift = append(drift, fmt.Sprintf("图注 %d（内置 %d）", cc, bc))
	}
	if cv != bv {
		drift = append(drift, fmt.Sprintf("图表块 %d（内置 %d）", cv, bv))
	}
	if len(drift) == 0 {
		return ""
	}
	return "与内置模板结构不一致：" + strings.Join(drift, "，")
}
