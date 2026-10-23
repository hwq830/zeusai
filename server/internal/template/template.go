// Package template manages briefing templates: the official library (seeded from
// prompts/briefing-templates) and users' private custom templates.
package template

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	texttemplate "text/template"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"

	"github.com/hwq830/zeusai/prompts"
	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/llm"
	"github.com/hwq830/zeusai/server/internal/moderation"
	"github.com/hwq830/zeusai/server/internal/schedule"
)

type Field struct {
	Key         string   `json:"key" yaml:"key"`
	Label       string   `json:"label" yaml:"label"`
	Type        string   `json:"type" yaml:"type"`
	Required    bool     `json:"required" yaml:"required"`
	Placeholder string   `json:"placeholder,omitempty" yaml:"placeholder"`
	Help        string   `json:"help,omitempty" yaml:"help"`
	Options     []string `json:"options,omitempty" yaml:"options"`
	Max         *int     `json:"max,omitempty" yaml:"max"`
	Min         *int     `json:"min,omitempty" yaml:"min"`
}

type Section struct {
	Key         string `json:"key" yaml:"key"`
	Title       string `json:"title" yaml:"title"`
	Instruction string `json:"instruction,omitempty" yaml:"instruction"`
	MaxItems    int    `json:"maxItems,omitempty" yaml:"max_items"`
}

type Source struct {
	Type  string `json:"type" yaml:"type"`
	Value string `json:"value" yaml:"value"`
	Label string `json:"label,omitempty" yaml:"label"`
}

// Input is the editable part of a template (TemplateInput in the contract).
type Input struct {
	Name               string             `json:"name" yaml:"name"`
	Description        string             `json:"description,omitempty" yaml:"description"`
	Category           string             `json:"category,omitempty" yaml:"category"`
	Icon               string             `json:"icon,omitempty" yaml:"icon"`
	Fields             []Field            `json:"fields" yaml:"fields"`
	Sections           []Section          `json:"sections" yaml:"sections"`
	Sources            []Source           `json:"sources,omitempty" yaml:"sources"`
	DefaultSchedule    *schedule.Schedule `json:"defaultSchedule,omitempty" yaml:"default_schedule"`
	DefaultLength      string             `json:"defaultLength,omitempty" yaml:"default_length"`
	RequiredConnectors []string           `json:"requiredConnectors,omitempty" yaml:"required_connectors"`
}

type Template struct {
	Input
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	Version   int        `json:"version"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
	OwnerID   *uuid.UUID `json:"-"`
}

var (
	keyRe       = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	fieldTypes  = map[string]bool{"text": true, "tags": true, "number": true, "select": true, "multiselect": true}
	sourceTypes = map[string]bool{"search": true, "site": true, "rss": true, "connector": true}
	lengths     = map[string]bool{"short": true, "standard": true, "long": true}
)

func (in *Input) Validate() error {
	if strings.TrimSpace(in.Name) == "" || len([]rune(in.Name)) > 40 {
		return apperr.Invalid("模板名称不能为空且不超过 40 字")
	}
	if len(in.Sections) == 0 || len(in.Sections) > 8 {
		return apperr.Invalid("模板需要 1–8 个栏目")
	}
	if len(in.Fields) > 12 {
		return apperr.Invalid("字段最多 12 个")
	}
	seen := map[string]bool{}
	for _, f := range in.Fields {
		if !keyRe.MatchString(f.Key) || seen[f.Key] {
			return apperr.Invalid(fmt.Sprintf("字段 key %q 无效或重复", f.Key))
		}
		seen[f.Key] = true
		if !fieldTypes[f.Type] {
			return apperr.Invalid(fmt.Sprintf("字段类型 %q 无效", f.Type))
		}
		if (f.Type == "select" || f.Type == "multiselect") && len(f.Options) == 0 {
			return apperr.Invalid(fmt.Sprintf("字段 %q 需要选项", f.Key))
		}
	}
	for i := range in.Sections {
		s := &in.Sections[i]
		if s.Key == "" {
			s.Key = fmt.Sprintf("s%d", i+1)
		}
		if strings.TrimSpace(s.Title) == "" {
			return apperr.Invalid("栏目标题不能为空")
		}
		if s.MaxItems <= 0 || s.MaxItems > 10 {
			s.MaxItems = 5
		}
	}
	for _, s := range in.Sources {
		if !sourceTypes[s.Type] || s.Value == "" {
			return apperr.Invalid("信息来源无效")
		}
	}
	if in.DefaultLength == "" {
		in.DefaultLength = "standard"
	}
	if !lengths[in.DefaultLength] {
		return apperr.Invalid("篇幅应为 short / standard / long")
	}
	if in.DefaultSchedule != nil {
		if err := in.DefaultSchedule.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Expand replaces {{field}} placeholders with the subscription's values; list values are
// joined with "、". A placeholder with no value expands to "".
func Expand(s string, values map[string]any) string {
	return placeholderRe.ReplaceAllStringFunc(s, func(m string) string {
		key := strings.TrimSpace(m[2 : len(m)-2])
		return ValueString(values[key])
	})
}

var placeholderRe = regexp.MustCompile(`\{\{\s*[a-z][a-z0-9_]*\s*\}\}`)

func ValueString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []any:
		parts := make([]string, 0, len(x))
		for _, p := range x {
			if s := ValueString(p); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "、")
	case []string:
		return strings.Join(x, "、")
	case float64:
		return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%f", x), "0"), ".")
	default:
		return fmt.Sprint(x)
	}
}

// FromRow converts a template row.
func FromRow(r db.Template) (Template, error) {
	t := Template{
		ID: r.ID.String(), Version: int(r.Version), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, OwnerID: r.OwnerID,
		Input: Input{
			Name: r.Name, Description: r.Description, Category: r.Category, Icon: r.Icon,
			DefaultLength: r.DefaultLength, RequiredConnectors: r.RequiredConnectors,
		},
	}
	t.Kind = "official"
	if r.OwnerID != nil {
		t.Kind = "custom"
	}
	for _, p := range []struct {
		raw json.RawMessage
		dst any
	}{{r.Fields, &t.Fields}, {r.Sections, &t.Sections}, {r.Sources, &t.Sources}} {
		if err := json.Unmarshal(p.raw, p.dst); err != nil {
			return t, err
		}
	}
	if len(r.DefaultSchedule) > 0 && string(r.DefaultSchedule) != "null" {
		var s schedule.Schedule
		if err := json.Unmarshal(r.DefaultSchedule, &s); err != nil {
			return t, err
		}
		t.DefaultSchedule = &s
	}
	return t, nil
}

type encoded struct {
	fields, sections, sources, sched json.RawMessage
}

func encode(in Input) encoded {
	m := func(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
	e := encoded{fields: m(orEmpty(in.Fields)), sections: m(orEmpty(in.Sections)), sources: m(orEmpty(in.Sources))}
	if in.DefaultSchedule != nil {
		e.sched = m(in.DefaultSchedule)
	}
	return e
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

type Service struct {
	Q   *db.Queries
	LLM llm.Provider
	Mod moderation.Checker
}

// texts is everything a user typed into a template, for moderation.
func (in Input) texts() []string {
	t := []string{in.Name, in.Description}
	for _, f := range in.Fields {
		t = append(t, f.Label)
	}
	for _, s := range in.Sections {
		t = append(t, s.Title, s.Instruction)
	}
	for _, s := range in.Sources {
		t = append(t, s.Value, s.Label)
	}
	return t
}

type ListParams struct {
	Scope, Category, Q string
	Limit, Offset      int
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (s *Service) List(ctx context.Context, userID uuid.UUID, p ListParams) ([]Template, []string, error) {
	var rows []db.Template
	var err error
	if p.Scope == "mine" {
		rows, err = s.Q.ListUserTemplates(ctx, db.ListUserTemplatesParams{OwnerID: &userID, Q: strPtr(p.Q), Limit: int32(p.Limit), Offset: int32(p.Offset)})
	} else {
		rows, err = s.Q.ListOfficialTemplates(ctx, db.ListOfficialTemplatesParams{Category: strPtr(p.Category), Q: strPtr(p.Q), Limit: int32(p.Limit), Offset: int32(p.Offset)})
	}
	if err != nil {
		return nil, nil, apperr.Internal(err)
	}
	cats, err := s.Q.ListOfficialCategories(ctx)
	if err != nil {
		return nil, nil, apperr.Internal(err)
	}
	out := make([]Template, 0, len(rows))
	for _, r := range rows {
		t, err := FromRow(r)
		if err != nil {
			return nil, nil, apperr.Internal(err)
		}
		out = append(out, t)
	}
	names := make([]string, 0, len(cats))
	for _, c := range cats {
		names = append(names, c.Category)
	}
	return out, names, nil
}

func (s *Service) Get(ctx context.Context, userID uuid.UUID, id string) (Template, error) {
	tid, err := uuid.Parse(id)
	if err != nil {
		return Template{}, apperr.NotFound("模板不存在")
	}
	r, err := s.Q.GetTemplateForUser(ctx, db.GetTemplateForUserParams{ID: tid, OwnerID: &userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Template{}, apperr.NotFound("模板不存在")
	}
	if err != nil {
		return Template{}, apperr.Internal(err)
	}
	t, err := FromRow(r)
	if err != nil {
		return Template{}, apperr.Internal(err)
	}
	return t, nil
}

const maxCustomTemplates = 20

func (s *Service) Create(ctx context.Context, userID uuid.UUID, in Input) (Template, error) {
	if err := in.Validate(); err != nil {
		return Template{}, err
	}
	if err := moderation.Require(ctx, s.Mod, in.texts()...); err != nil {
		return Template{}, err
	}
	existing, err := s.Q.ListUserTemplates(ctx, db.ListUserTemplatesParams{OwnerID: &userID, Limit: maxCustomTemplates, Offset: 0})
	if err != nil {
		return Template{}, apperr.Internal(err)
	}
	if len(existing) >= maxCustomTemplates {
		return Template{}, apperr.QuotaExceeded("自定义模板最多 20 个")
	}
	e := encode(in)
	r, err := s.Q.CreateTemplate(ctx, db.CreateTemplateParams{
		OwnerID: &userID, Name: in.Name, Description: in.Description, Category: in.Category, Icon: in.Icon,
		Fields: e.fields, Sections: e.sections, Sources: e.sources, DefaultSchedule: e.sched,
		DefaultLength: in.DefaultLength, RequiredConnectors: orEmpty(in.RequiredConnectors),
	})
	if err != nil {
		return Template{}, apperr.Internal(err)
	}
	t, _ := FromRow(r)
	return t, nil
}

func (s *Service) Update(ctx context.Context, userID uuid.UUID, id string, in Input) (Template, error) {
	if err := in.Validate(); err != nil {
		return Template{}, err
	}
	if err := moderation.Require(ctx, s.Mod, in.texts()...); err != nil {
		return Template{}, err
	}
	tid, err := uuid.Parse(id)
	if err != nil {
		return Template{}, apperr.NotFound("模板不存在")
	}
	e := encode(in)
	r, err := s.Q.UpdateUserTemplate(ctx, db.UpdateUserTemplateParams{
		ID: tid, OwnerID: &userID, Name: in.Name, Description: in.Description, Category: in.Category, Icon: in.Icon,
		Fields: e.fields, Sections: e.sections, Sources: e.sources, DefaultSchedule: e.sched,
		DefaultLength: in.DefaultLength, RequiredConnectors: orEmpty(in.RequiredConnectors),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Template{}, apperr.NotFound("模板不存在或不可修改")
	}
	if err != nil {
		return Template{}, apperr.Internal(err)
	}
	t, _ := FromRow(r)
	return t, nil
}

func (s *Service) Delete(ctx context.Context, userID uuid.UUID, id string) error {
	tid, err := uuid.Parse(id)
	if err != nil {
		return apperr.NotFound("模板不存在")
	}
	n, err := s.Q.DeleteUserTemplate(ctx, db.DeleteUserTemplateParams{ID: tid, OwnerID: &userID})
	if err != nil {
		return apperr.Internal(err)
	}
	if n == 0 {
		return apperr.NotFound("模板不存在或不可删除")
	}
	return nil
}

func (s *Service) Copy(ctx context.Context, userID uuid.UUID, id string) (Template, error) {
	t, err := s.Get(ctx, userID, id)
	if err != nil {
		return Template{}, err
	}
	in := t.Input
	in.Name = truncate(in.Name+"（副本）", 40)
	return s.Create(ctx, userID, in)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

type seedFile struct {
	Input     `yaml:",inline"`
	Slug      string `yaml:"slug"`
	SortOrder int    `yaml:"sort_order"`
}

// SeedOfficial upserts every prompts/briefing-templates/*.yaml by slug. Version bumps when
// fields, sections or sources change.
func (s *Service) SeedOfficial(ctx context.Context) (int, error) {
	files, err := fs.Glob(prompts.FS, "briefing-templates/*.yaml")
	if err != nil {
		return 0, err
	}
	for _, f := range files {
		b, err := prompts.FS.ReadFile(f)
		if err != nil {
			return 0, err
		}
		var sf seedFile
		if err := yaml.Unmarshal(b, &sf); err != nil {
			return 0, fmt.Errorf("%s: %w", f, err)
		}
		if err := sf.Input.Validate(); err != nil {
			return 0, fmt.Errorf("%s: %w", f, err)
		}
		e := encode(sf.Input)
		if err := s.Q.UpsertOfficialTemplate(ctx, db.UpsertOfficialTemplateParams{
			Slug: &sf.Slug, Name: sf.Name, Description: sf.Description, Category: sf.Category, Icon: sf.Icon,
			Fields: e.fields, Sections: e.sections, Sources: e.sources, DefaultSchedule: e.sched,
			DefaultLength: sf.DefaultLength, RequiredConnectors: orEmpty(sf.RequiredConnectors), SortOrder: int32(sf.SortOrder),
		}); err != nil {
			return 0, fmt.Errorf("%s: %w", f, err)
		}
	}
	return len(files), nil
}

// Draft turns one sentence into a template draft; nothing is saved.
func (s *Service) Draft(ctx context.Context, userID uuid.UUID, tz, prompt string) (Input, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return Input{}, apperr.Invalid("请描述你想要的简报模板")
	}
	if err := moderation.Require(ctx, s.Mod, prompt); err != nil {
		return Input{}, err
	}
	sys, err := renderPrompt("briefing/draft_template.md", map[string]any{"TZ": tz})
	if err != nil {
		return Input{}, apperr.Internal(err)
	}
	var in Input
	if _, err := llm.CompleteJSON(ctx, s.LLM, llm.Request{
		Purpose: "template.draft", Tier: llm.TierStrong, System: sys,
		Messages: []llm.Message{{Role: "user", Content: prompt}}, UserID: &userID,
	}, &in); err != nil {
		return Input{}, apperr.Unavailable("没能生成模板，换个说法试试").WithCause(err)
	}
	if in.DefaultSchedule != nil {
		in.DefaultSchedule.TZ = tz
	}
	if err := in.Validate(); err != nil {
		return Input{}, apperr.Unavailable("没能生成可用的模板，换个说法试试").WithCause(err)
	}
	return in, nil
}

func renderPrompt(name string, data any) (string, error) {
	t, err := texttemplate.ParseFS(prompts.FS, name)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	err = t.Execute(&b, data)
	return b.String(), err
}
