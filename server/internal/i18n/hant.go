package i18n

import (
	"strings"
	"sync"

	"github.com/longbridgeapp/opencc"
)

// Traditional Chinese is not written by hand: the Chinese (Simplified) source text is converted
// with OpenCC. Fixed texts (errors, step titles, pushes, templates) use s2twp, which also swaps
// in Taiwan wording (设置 → 設定, 登录 → 登入); hantFixes then undoes the swaps that are wrong
// for this app. The apps' zh-Hant.json tables are generated with the same rules
// (zeusai-ios scripts/gen_zh_hant.py). Model output only gets s2tw, a character-level pass,
// so the model's own wording is kept and names in news are not rewritten.
var hantFixes = strings.NewReplacer(
	"登出", "註銷", "賬號", "帳號", "賬戶", "帳戶", "型別", "類型", "擴充套件", "擴充",
	"聯結器", "連接器", "微訊號", "微信號", "分鍾", "分鐘", "加載", "載入", "反饋", "回饋",
	"質量", "品質", "自定義", "自訂", "釋出", "發布",
)

var (
	ccOnce            sync.Once
	ccPhrase, ccPlain *opencc.OpenCC
)

func converters() (*opencc.OpenCC, *opencc.OpenCC) {
	ccOnce.Do(func() {
		ccPhrase, _ = opencc.New("s2twp")
		ccPlain, _ = opencc.New("s2tw")
	})
	return ccPhrase, ccPlain
}

// Hant converts a fixed Chinese (Simplified) text to Traditional Chinese with Taiwan wording.
// On a converter failure the text is returned unchanged: Simplified is readable, an error is not.
func Hant(s string) string {
	cc, _ := converters()
	if s == "" || cc == nil {
		return s
	}
	out, err := cc.Convert(s)
	if err != nil {
		return s
	}
	return hantFixes.Replace(out)
}

// HantChars converts only the characters of a text (no wording changes). It is used on model
// output that was asked to be in Traditional Chinese, to catch Simplified characters that slip in.
func HantChars(s string) string {
	_, cc := converters()
	if s == "" || cc == nil {
		return s
	}
	out, err := cc.Convert(s)
	if err != nil {
		return s
	}
	return out
}
