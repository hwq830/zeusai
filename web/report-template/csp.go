package reporttemplate

import (
	"crypto/sha256"
	"encoding/base64"
	"regexp"
	"strings"
)

var inlineScriptRe = regexp.MustCompile(`(?s)<script>(.*?)</script>`)

// Page template file names. A page-template version may only contain these plus usage/notices.
const (
	TemplateFile    = "report.html.tmpl"
	ChartScriptFile = "chartscript.js"
	UsageFile       = "usage.md"
	NoticesFile     = "notices.md"
)

// ContractVersion pins the Data shape a stored template is written against. A version with a
// different contract is rejected rather than rendering half a page.
const ContractVersion = "report-template/v1"

// ComputeCSPHashes hashes every inline script the rendered page will contain: the chart
// script, the fixed shell script, and the scripts already inline in the template. Template
// expressions are skipped because the executed value is what the browser hashes.
func ComputeCSPHashes(templateText, chartScript string) []string {
	hashes := []string{"sha256-" + visualChartScriptHash}
	if chartScript != "" {
		sum := sha256.Sum256([]byte(chartScript))
		hashes[0] = "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
	}
	hashes = append(hashes, "sha256-"+visualShellScriptHash)
	seen := map[string]bool{}
	for _, h := range hashes {
		seen[h] = true
	}
	for _, match := range inlineScriptRe.FindAllStringSubmatch(templateText, -1) {
		body := match[1]
		if strings.Contains(body, "{{") {
			continue
		}
		sum := sha256.Sum256([]byte(body))
		hash := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
		if !seen[hash] {
			seen[hash] = true
			hashes = append(hashes, hash)
		}
	}
	return hashes
}

// CSPFromHashes builds the report page CSP from the hashes stored with the version.
func CSPFromHashes(hashes []string) string {
	quoted := make([]string, 0, len(hashes))
	for _, h := range hashes {
		quoted = append(quoted, "'"+h+"'")
	}
	return "default-src 'none'; style-src 'unsafe-inline'; img-src data:; font-src data:; script-src " + strings.Join(quoted, " ")
}

// visualCSP allows exactly the fixed chart script and the fixed inline UI
// scripts that are already in report.html.tmpl. Data never enters a hashed
// script; it lives in the application/json block.
var visualCSP = CSPFromHashes(ComputeCSPHashes(src, visualChartScript))
