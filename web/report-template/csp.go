package reporttemplate

import (
	"crypto/sha256"
	"encoding/base64"
	"regexp"
	"strings"
)

var inlineScriptRe = regexp.MustCompile(`(?s)<script>(.*?)</script>`)

// visualCSP allows exactly the fixed chart script and the fixed inline UI
// scripts that are already in report.html.tmpl. Data never enters a hashed
// script; it lives in the application/json block.
var visualCSP = func() string {
	hashes := []string{"'sha256-" + visualChartScriptHash + "'", "'sha256-" + visualShellScriptHash + "'"}
	seen := map[string]bool{hashes[0]: true, hashes[1]: true}
	for _, match := range inlineScriptRe.FindAllStringSubmatch(src, -1) {
		body := match[1]
		if strings.Contains(body, "{{") {
			continue
		}
		sum := sha256.Sum256([]byte(body))
		hash := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
		if !seen[hash] {
			seen[hash] = true
			hashes = append(hashes, hash)
		}
	}
	return "default-src 'none'; style-src 'unsafe-inline'; img-src data:; font-src data:; script-src " + strings.Join(hashes, " ")
}()
