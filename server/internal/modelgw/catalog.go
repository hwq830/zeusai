package modelgw

import (
	_ "embed"
	"encoding/json"
	"net/url"
	"strings"
)

// PriceTier is one band of a price that depends on the prompt length; MaxTokens is the
// band's upper bound in input tokens.
type PriceTier struct {
	Name      string `json:"name"`
	MaxTokens int    `json:"maxTokens,omitempty"`
	Price     Price  `json:"price"`
}

// CatalogModel is a text-generation model Alibaba Cloud Bailian (Model Studio) offers, with
// its list price: Price when it has a single one, Tiers when it depends on prompt length.
type CatalogModel struct {
	Model         string      `json:"model"`
	Vendor        string      `json:"vendor"`
	ContextWindow int         `json:"contextWindow"`
	Price         *Price      `json:"price,omitempty"`
	Tiers         []PriceTier `json:"tiers,omitempty"`
}

// Metered is the price cost metering uses: the single price, else the first (shortest
// prompt) tier, which covers nearly every call this product makes.
func (m CatalogModel) Metered() (Price, bool) {
	if m.Price != nil {
		return *m.Price, true
	}
	if len(m.Tiers) > 0 {
		return m.Tiers[0].Price, true
	}
	return Price{}, false
}

type catalog struct {
	Source  string         `json:"source"`
	Updated string         `json:"updated"`
	Models  []CatalogModel `json:"models"`
}

// catalog_bailian.json is the built-in copy of Bailian's model list and prices, used until
// an admin syncs live prices on the console's pricing page (stored in model_prices).
//
//go:embed catalog_bailian.json
var bailianJSON []byte

var bailian = func() catalog {
	var c catalog
	if err := json.Unmarshal(bailianJSON, &c); err != nil {
		panic("modelgw: bad catalog_bailian.json: " + err.Error())
	}
	return c
}()

// BailianCatalog returns the catalog and the date it was taken.
func BailianCatalog() ([]CatalogModel, string) { return bailian.Models, bailian.Updated }

// IsBailian reports whether baseURL is a Bailian (DashScope) endpoint, so the catalog applies.
func IsBailian(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	h := u.Hostname()
	return h == "dashscope.aliyuncs.com" || h == "dashscope-intl.aliyuncs.com" || strings.HasSuffix(h, ".maas.aliyuncs.com")
}

// BailianPrice is the built-in catalog's metered price for model.
func BailianPrice(model string) (Price, bool) {
	for _, m := range bailian.Models {
		if m.Model == model {
			return m.Metered()
		}
	}
	return Price{}, false
}

// BailianSource describes where the built-in catalog came from.
func BailianSource() string { return bailian.Source }
