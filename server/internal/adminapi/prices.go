package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/llm"
	"github.com/hwq830/zeusai/server/internal/modelgw"
)

// priceRow mirrors gen.ModelPriceRow.
type priceRow struct {
	Model         string              `json:"model"`
	Vendor        string              `json:"vendor"`
	ContextWindow int                 `json:"contextWindow"`
	Tiers         []modelgw.PriceTier `json:"tiers"`
	Manual        *modelgw.Price      `json:"manual,omitempty"`
	Metered       *modelgw.Price      `json:"metered,omitempty"`
	Origin        string              `json:"origin"`
	Stale         bool                `json:"stale"`
	UsedBy        []string            `json:"usedBy"`
	Note          string              `json:"note"`
	UpdatedBy     *string             `json:"updatedBy,omitempty"`
	UpdatedAt     *time.Time          `json:"updatedAt,omitempty"`
}

type priceList struct {
	Source         string     `json:"source"`
	SyncedAt       *time.Time `json:"syncedAt,omitempty"`
	BuiltinUpdated string     `json:"builtinUpdated"`
	Models         []priceRow `json:"models"`
}

// metered maps each model to the price cost metering uses on Bailian endpoints.
func (l priceList) metered() map[string]modelgw.Price {
	out := map[string]modelgw.Price{}
	for _, r := range l.Models {
		if r.Metered != nil {
			out[r.Model] = *r.Metered
		}
	}
	return out
}

// prices lists the pricing page: synced rows once a sync has run, otherwise the built-in
// catalog; admin prices are laid over either.
func (s *Server) prices(ctx context.Context) (priceList, error) {
	rows, err := s.Q.ListModelPrices(ctx)
	if err != nil {
		return priceList{}, apperr.Internal(err)
	}
	usedBy, err := s.routingModels(ctx)
	if err != nil {
		return priceList{}, apperr.Internal(err)
	}
	builtin, updated := modelgw.BailianCatalog()
	out := priceList{Source: "builtin", BuiltinUpdated: updated}
	for _, r := range rows {
		if r.SyncedAt != nil && (out.SyncedAt == nil || r.SyncedAt.After(*out.SyncedAt)) {
			t := *r.SyncedAt
			out.SyncedAt = &t
		}
	}
	// index maps a model to its position in out.Models (pointers would go stale on append).
	index := map[string]int{}
	add := func(p priceRow) {
		p.UsedBy = usedBy[p.Model]
		if p.UsedBy == nil {
			p.UsedBy = []string{}
		}
		index[p.Model] = len(out.Models)
		out.Models = append(out.Models, p)
	}
	if out.SyncedAt == nil {
		for _, m := range builtin {
			tiers := m.Tiers
			if tiers == nil && m.Price != nil {
				tiers = []modelgw.PriceTier{{Price: *m.Price}}
			}
			add(priceRow{Model: m.Model, Vendor: m.Vendor, ContextWindow: m.ContextWindow, Tiers: tiers})
		}
	} else {
		out.Source = "synced"
	}
	for _, r := range rows {
		var tiers []modelgw.PriceTier
		_ = json.Unmarshal(r.Tiers, &tiers)
		i, ok := index[r.Model]
		if !ok {
			add(priceRow{Model: r.Model})
			i = len(out.Models) - 1
		}
		p := &out.Models[i]
		if r.SyncedAt != nil {
			p.Vendor, p.ContextWindow, p.Tiers = r.Vendor, int(r.ContextWindow), tiers
			p.Stale = r.SyncedAt.Before(*out.SyncedAt)
		}
		if r.ManualInput != nil {
			m := modelgw.Price{Input: *r.ManualInput, Output: deref(r.ManualOutput), Cached: deref(r.ManualCached)}
			p.Manual, p.Note, p.UpdatedBy = &m, r.Note, r.UpdatedByEmail
			t := r.UpdatedAt
			p.UpdatedAt = &t
		}
	}
	for i := range out.Models {
		p := &out.Models[i]
		if p.Tiers == nil {
			p.Tiers = []modelgw.PriceTier{}
		}
		switch {
		case p.Manual != nil:
			m := *p.Manual
			p.Metered, p.Origin = &m, "manual"
		case len(p.Tiers) > 0:
			m := p.Tiers[0].Price
			p.Metered, p.Origin = &m, "synced"
			if out.SyncedAt == nil {
				p.Origin = "builtin"
			}
		default:
			p.Origin = "none"
		}
	}
	slices.SortStableFunc(out.Models, func(a, b priceRow) int { return strings.Compare(a.Model, b.Model) })
	return out, nil
}

// routingModels maps each model the routing calls to where: tiers (a step's model, or its
// provider's default) and purposes.
func (s *Server) routingModels(ctx context.Context) (map[string][]string, error) {
	out := map[string][]string{}
	row, err := s.Q.GetGatewaySettings(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		defaults := map[string]map[llm.Tier]string{}
		provs, err := s.Q.ListModelProviders(ctx)
		if err != nil {
			return nil, err
		}
		for _, p := range provs {
			defaults[p.ModelProvider.Name] = modelgw.ConsoleProvider(p.ModelProvider).Models
		}
		st := modelgw.ParseSettings(row.GatewaySetting)
		for _, t := range modelgw.Tiers {
			for _, step := range st.Routes[t] {
				m := step.Model
				if m == "" {
					m = defaults[step.Provider][t]
				}
				if m != "" && !slices.Contains(out[m], string(t)) {
					out[m] = append(out[m], string(t))
				}
			}
		}
	}
	ovs, err := s.Q.ListPurposeOverrides(ctx)
	if err != nil {
		return nil, err
	}
	for _, o := range ovs {
		out[o.Model] = append(out[o.Model], o.Scope)
	}
	return out, nil
}

func (s *Server) ListModelPrices(ctx context.Context, _ gen.ListModelPricesRequestObject) (gen.ListModelPricesResponseObject, error) {
	l, err := s.prices(ctx)
	if err != nil {
		return nil, err
	}
	return gen.ListModelPrices200JSONResponse(recode[gen.ModelPriceList](l)), nil
}

func (s *Server) SetModelPrice(ctx context.Context, req gen.SetModelPriceRequestObject) (gen.SetModelPriceResponseObject, error) {
	a, err := require(ctx, PermModels)
	if err != nil {
		return nil, err
	}
	b := req.Body
	model := strings.TrimSpace(b.Model)
	if model == "" || len([]rune(model)) > 120 {
		return nil, apperr.Invalid("请填写模型名（不超过 120 个字符）")
	}
	pr := b.Price
	if pr.Input < 0 || pr.Output < 0 || pr.Cached < 0 {
		return nil, apperr.Invalid("价格不能为负")
	}
	note := strings.TrimSpace(deref(b.Note))
	if len([]rune(note)) > 200 {
		return nil, apperr.Invalid("备注不能超过 200 个字符")
	}
	in, outp, cached := float64(pr.Input), float64(pr.Output), float64(pr.Cached)
	if err := s.Q.SetManualModelPrice(ctx, db.SetManualModelPriceParams{Model: model, ManualInput: &in, ManualOutput: &outp,
		ManualCached: &cached, Note: note, UpdatedBy: &a.ID}); err != nil {
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "model_price.set", "model_price", model, map[string]any{"price": pr, "note": note})
	l, err := s.prices(ctx)
	if err != nil {
		return nil, err
	}
	return gen.SetModelPrice200JSONResponse(recode[gen.ModelPriceList](l)), nil
}

func (s *Server) ClearModelPrice(ctx context.Context, req gen.ClearModelPriceRequestObject) (gen.ClearModelPriceResponseObject, error) {
	a, err := require(ctx, PermModels)
	if err != nil {
		return nil, err
	}
	model := req.Params.Model
	n, err := s.Q.ClearManualModelPrice(ctx, db.ClearManualModelPriceParams{Model: model, UpdatedBy: &a.ID})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	if _, err := s.Q.DeleteUnsyncedModelPrice(ctx, model); err != nil {
		return nil, apperr.Internal(err)
	}
	if n > 0 {
		s.audit(ctx, "model_price.clear", "model_price", model, nil)
	}
	return gen.ClearModelPrice204Response{}, nil
}

// SyncModelPrices pulls Bailian's live model list and prices; admin prices are kept.
func (s *Server) SyncModelPrices(ctx context.Context, _ gen.SyncModelPricesRequestObject) (gen.SyncModelPricesResponseObject, error) {
	if _, err := require(ctx, PermModels); err != nil {
		return nil, err
	}
	before, err := s.Q.ListModelPrices(ctx)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	fctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	models, err := modelgw.FetchBailianCatalog(fctx, &http.Client{Timeout: 30 * time.Second}, s.BailianCatalogURL)
	if err != nil {
		return gen.SyncModelPrices200JSONResponse{Message: "同步失败，价格没有变：" + err.Error()}, nil
	}
	prev := map[string]string{}
	for _, r := range before {
		if r.SyncedAt != nil {
			prev[r.Model] = string(r.Tiers)
		}
	}
	now := time.Now()
	added, changed := 0, []string{}
	for _, m := range models {
		tiers := m.Tiers
		if tiers == nil {
			tiers = []modelgw.PriceTier{}
		}
		tj, _ := json.Marshal(tiers)
		if old, ok := prev[m.Model]; !ok {
			added++
		} else if !sameJSON(old, tj) {
			changed = append(changed, m.Model)
		}
		if err := s.Q.SyncModelPrice(ctx, db.SyncModelPriceParams{Model: m.Model, Vendor: m.Vendor,
			ContextWindow: int32(m.ContextWindow), Tiers: tj, SyncedAt: &now}); err != nil {
			return nil, apperr.Internal(err)
		}
	}
	if len(prev) == 0 {
		added = len(models)
	}
	s.audit(ctx, "model_price.sync", "model_price", "bailian", map[string]any{"count": len(models), "added": added, "changed": changed})
	count := len(models)
	msg := fmt.Sprintf("已从百炼同步 %d 个模型", count)
	if len(prev) > 0 {
		msg += fmt.Sprintf("，新增 %d 个，标价变化 %d 个", added, len(changed))
	}
	return gen.SyncModelPrices200JSONResponse{Ok: true, Message: msg, Count: &count, Added: &added, Changed: &changed}, nil
}

func sameJSON(a string, b []byte) bool {
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	xb, _ := json.Marshal(x)
	yb, _ := json.Marshal(y)
	return string(xb) == string(yb)
}
