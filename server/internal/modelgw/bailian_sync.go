package modelgw

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// BailianCatalogURL is the model list the Bailian console and its CLI (`bl model list`) read.
// It answers without credentials and carries list prices. It is not a documented API, so
// callers keep the built-in catalog as a fallback.
const BailianCatalogURL = "https://bailian-cs.console.aliyun.com/cli/api.json?action=BroadScopeAspnGateway" +
	"&product=sfm_bailian&api=zeldaHttp.dashscopeModel.%2Fzelda%2Fapi%2Fv1%2FmodelCenter%2FlistFoundationModels"

const bailianPageSize = 50

// FetchBailianCatalog downloads every text-generation model Bailian lists, with its prices
// (CNY per million tokens), from endpoint (BailianCatalogURL unless testing).
func FetchBailianCatalog(ctx context.Context, client *http.Client, endpoint string) ([]CatalogModel, error) {
	if endpoint == "" {
		endpoint = BailianCatalogURL
	}
	var out []CatalogModel
	seen := map[string]bool{}
	for page := 1; page <= 40; page++ {
		pg, err := fetchBailianPage(ctx, client, endpoint, page)
		if err != nil {
			return nil, err
		}
		// total counts groups ("group-qwen3.8-max"), each holding one or more models.
		for _, g := range pg.List {
			items := g.Items
			if len(items) == 0 && !strings.HasPrefix(g.Model, "group-") {
				items = []bailianItem{g.bailianItem}
			}
			for _, it := range items {
				if it.Model == "" || seen[it.Model] {
					continue
				}
				seen[it.Model] = true
				out = append(out, it.catalogModel())
			}
		}
		if len(pg.List) == 0 || page*bailianPageSize >= int(pg.Total) {
			break
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("百炼没有返回任何模型")
	}
	return out, nil
}

func fetchBailianPage(ctx context.Context, client *http.Client, endpoint string, page int) (bailianPage, error) {
	params := map[string]any{
		"Api": "zeldaHttp.dashscopeModel./zelda/api/v1/modelCenter/listFoundationModels",
		"V":   "1.0",
		"Data": map[string]any{
			"input": map[string]any{"pageNo": page, "pageSize": bailianPageSize, "group": true, "queryPrice": true,
				"capabilities": []string{"TG"}, "providers": []string{}, "inferenceProviders": []string{},
				"features": []string{}, "contextWindows": []string{}, "name": ""},
			"cornerstoneParam": map[string]any{"protocol": "V2", "console": "ONE_CONSOLE", "productCode": "p_efm",
				"switchUserType": 3, "consoleSite": "BAILIAN_ALIYUN"},
		},
	}
	pj, _ := json.Marshal(params)
	form := url.Values{"params": {string(pj)}, "region": {"cn-beijing"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return bailianPage{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return bailianPage{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return bailianPage{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return bailianPage{}, fmt.Errorf("百炼返回 HTTP %d", resp.StatusCode)
	}
	return parseBailianPage(body)
}

// parseBailianPage reads data.DataV2.data.data, or data.DataV2.data in older answers.
func parseBailianPage(body []byte) (bailianPage, error) {
	var env struct {
		Data struct {
			DataV2 struct {
				Data json.RawMessage `json:"data"`
			} `json:"DataV2"`
		} `json:"data"`
		Message string `json:"message"`
		Code    any    `json:"code"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return bailianPage{}, fmt.Errorf("百炼返回的内容无法解析：%w", err)
	}
	raw := env.Data.DataV2.Data
	if len(raw) == 0 || string(raw) == "null" {
		msg := env.Message
		if msg == "" {
			msg = "格式和预期不同"
		}
		return bailianPage{}, fmt.Errorf("百炼返回的内容里没有模型列表（%s）", msg)
	}
	var inner struct {
		Data *bailianPage `json:"data"`
	}
	if json.Unmarshal(raw, &inner) == nil && inner.Data != nil && inner.Data.List != nil {
		return *inner.Data, nil
	}
	var pg bailianPage
	if err := json.Unmarshal(raw, &pg); err != nil {
		return bailianPage{}, fmt.Errorf("百炼返回的模型列表无法解析：%w", err)
	}
	return pg, nil
}

type bailianPage struct {
	Total flexNum        `json:"total"`
	List  []bailianGroup `json:"list"`
}

type bailianGroup struct {
	bailianItem
	Items []bailianItem `json:"items"`
}

type bailianItem struct {
	Model         string         `json:"model"`
	Provider      string         `json:"provider"`
	ContextWindow flexNum        `json:"contextWindow"`
	Prices        []bailianPrice `json:"prices"`
	MultiPrices   []struct {
		RangeStart flexNum        `json:"rangeStart"`
		RangeEnd   flexNum        `json:"rangeEnd"`
		RangeName  string         `json:"rangeName"`
		Prices     []bailianPrice `json:"prices"`
	} `json:"multiPrices"`
}

type bailianPrice struct {
	Type     string  `json:"type"`
	Price    flexNum `json:"price"`
	TimeBand string  `json:"timeBand"`
}

func (it bailianItem) catalogModel() CatalogModel {
	m := CatalogModel{Model: it.Model, Vendor: it.Provider, ContextWindow: int(it.ContextWindow)}
	if p, ok := standardPrice(it.Prices); ok {
		m.Tiers = []PriceTier{{Price: p}}
	}
	for _, r := range it.MultiPrices {
		if p, ok := standardPrice(r.Prices); ok {
			m.Tiers = append(m.Tiers, PriceTier{Name: tierName(int(r.RangeStart), int(r.RangeEnd), r.RangeName), MaxTokens: int(r.RangeEnd), Price: p})
		}
	}
	return m
}

// tierName labels a prompt-length band as ≤128K or 128K–256K, falling back to Bailian's name.
func tierName(start, end int, name string) string {
	if end <= 0 {
		return name
	}
	if start <= 0 {
		return "≤" + tokenSize(end)
	}
	return tokenSize(start) + "–" + tokenSize(end)
}

// tokenSize writes 131072 as 128K and 1000000 as 1M.
func tokenSize(n int) string {
	if n >= 1_000_000 && n%1_000_000 < 50_000 {
		return strconv.Itoa(n/1_000_000) + "M"
	}
	return strconv.Itoa((n+512)/1024) + "K"
}

// standardPrice picks the non-batch, standard-hours input, output and cache-hit prices; a
// missing cache price counts as the input price, so cached tokens are never metered at 0.
func standardPrice(ps []bailianPrice) (Price, bool) {
	var p Price
	var in, outp, cached bool
	for _, x := range ps {
		if x.TimeBand != "" && x.TimeBand != "standard" {
			continue
		}
		switch x.Type {
		case "input_token":
			p.Input, in = float64(x.Price), true
		case "output_token":
			p.Output, outp = float64(x.Price), true
		case "input_token_cache":
			p.Cached, cached = float64(x.Price), true
		}
	}
	if !in || !outp {
		return Price{}, false
	}
	if !cached {
		p.Cached = p.Input
	}
	return p, true
}

// flexNum accepts a JSON number, a numeric string or null.
type flexNum float64

func (f *flexNum) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		*f = 0
		return nil
	}
	*f = flexNum(v)
	return nil
}
