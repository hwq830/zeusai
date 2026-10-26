package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestMessage(t *testing.T) {
	for _, c := range []struct {
		lang      Lang
		code, msg string
		want      string
	}{
		{ZH, "not_found", "报告不存在", "报告不存在"},
		{EN, "not_found", "报告不存在", "Report not found"},
		{EN, "invalid_argument", `字段 key "a b" 无效或重复`, "Field key a b is invalid or used twice"},
		{EN, "quota_exceeded", "本月 token 额度已用完，11 月 1 日重置", "You've used this month's tokens. They reset on November 1"},
		{EN, "invalid_argument", "没见过的提示", codeEN["invalid_argument"]},
		{EN, "weird", "没见过的提示", codeEN["internal"]},
	} {
		if got := Message(c.lang, c.code, c.msg); got != c.want {
			t.Errorf("Message(%s, %q) = %q, want %q", c.lang, c.msg, got, c.want)
		}
	}
}

func TestParse(t *testing.T) {
	for in, want := range map[string]Lang{"en": EN, "en-US": EN, "EN_gb": EN, "zh-Hans-CN": ZH, "": ZH, "ja": ZH} {
		if got := Parse(in); got != want {
			t.Errorf("Parse(%q) = %s", in, got)
		}
	}
	if got := FromAcceptLanguage("en-GB;q=0.9, zh-Hans;q=0.8"); got != EN {
		t.Errorf("FromAcceptLanguage = %s", got)
	}
	if ReportLang("auto", "en") != EN || ReportLang("zh", "en") != ZH || ReportLang("", "zh") != ZH {
		t.Error("ReportLang")
	}
}

var (
	han = regexp.MustCompile(`\p{Han}`)
	// Error constructors whose message reaches App users; New takes it as the third argument.
	msgArg = map[string]int{
		"Invalid": 0, "NotFound": 0, "Forbidden": 0, "Conflict": 0, "RateLimited": 0, "Unavailable": 0,
		"QuotaExceeded": 0, "Unauthenticated": 0, "New": 2,
	}
	// The admin console is Chinese-only.
	adminOnly = []string{"adminapi/", "remoteconfig/", "notify/campaign.go", "i18n/", "db/", "/gen/"}
)

// TestCatalogCoversErrors finds every apperr constructor with a Chinese message outside the
// admin-only code and checks that it has an English text.
func TestCatalogCoversErrors(t *testing.T) {
	root := ".."
	fset := token.NewFileSet()
	checked := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for _, skip := range adminOnly {
			if strings.HasPrefix(rel, skip) || strings.Contains("/"+rel, skip) {
				return nil
			}
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			idx := -1
			switch fn := call.Fun.(type) {
			case *ast.SelectorExpr:
				if x, ok := fn.X.(*ast.Ident); ok && x.Name == "apperr" {
					if i, ok := msgArg[fn.Sel.Name]; ok {
						idx = i
					}
				}
			case *ast.Ident:
				if fn.Name == "exceeded" { // quota's helper
					idx = 0
				}
			}
			if idx < 0 || idx >= len(call.Args) {
				return true
			}
			key, ok := messageKey(call.Args[idx])
			if !ok || !han.MatchString(key) {
				return true
			}
			checked++
			sample := regexp.MustCompile(`%[sdq]`).ReplaceAllString(key, "1")
			if Message(EN, "no_such_code", sample) == codeEN["internal"] {
				t.Errorf("%s: no English text for %q; add it to messagesEN", fset.Position(call.Pos()), key)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 50 {
		t.Fatalf("only %d messages found; is the scan broken?", checked)
	}
}

// messageKey turns a message expression into its catalog key: literals as written, every
// other operand as %s, and fmt.Sprintf by its format.
func messageKey(e ast.Expr) (string, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(x.Value)
		return s, err == nil
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return "", false
		}
		l, lok := messageKey(x.X)
		r, rok := messageKey(x.Y)
		if !lok {
			l = "%s"
		}
		if !rok {
			r = "%s"
		}
		return l + r, lok || rok
	case *ast.CallExpr:
		if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Sprintf" && len(x.Args) > 0 {
			return messageKey(x.Args[0])
		}
	}
	return "", false
}
