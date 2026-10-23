// Package connector lists built-in tools and whitelisted MCP connectors with each user's
// on/off state and authorization.
package connector

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"

	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
)

//go:embed catalog.yaml
var catalogYAML []byte

type Tool struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description"`
	Write       bool   `json:"write" yaml:"write"`
}

type Connector struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	IconURL      string `json:"iconUrl,omitempty"`
	Kind         string `json:"kind"`
	Category     string `json:"category,omitempty"`
	Enabled      bool   `json:"enabled"`
	RequiresAuth bool   `json:"requiresAuth"`
	Authorized   bool   `json:"authorized"`
	Tools        []Tool `json:"tools"`
}

type catalogEntry struct {
	ID           string `yaml:"id"`
	Name         string `yaml:"name"`
	Description  string `yaml:"description"`
	IconURL      string `yaml:"icon_url"`
	Kind         string `yaml:"kind"`
	Category     string `yaml:"category"`
	RequiresAuth bool   `yaml:"requires_auth"`
	DefaultOn    bool   `yaml:"default_on"`
	Transport    string `yaml:"transport"`
	EndpointEnv  string `yaml:"endpoint_env"` // http transport: env var with the server URL
	Tools        []Tool `yaml:"tools"`
}

func catalog() ([]catalogEntry, error) {
	var entries []catalogEntry
	return entries, yaml.Unmarshal(catalogYAML, &entries)
}

type Service struct{ Q *db.Queries }

// SeedCatalog upserts catalog.yaml into the connectors table.
func (s *Service) SeedCatalog(ctx context.Context) (int, error) {
	entries, err := catalog()
	if err != nil {
		return 0, err
	}
	for i, e := range entries {
		tools, _ := json.Marshal(e.Tools)
		if e.Transport == "" {
			e.Transport = "builtin"
		}
		if err := s.Q.UpsertConnector(ctx, db.UpsertConnectorParams{
			ID: e.ID, Name: e.Name, Description: e.Description, IconUrl: e.IconURL, Kind: e.Kind, Category: e.Category,
			RequiresAuth: e.RequiresAuth, DefaultOn: e.DefaultOn, Tools: tools, Transport: e.Transport,
			Available: true, SortOrder: int32(i),
		}); err != nil {
			return 0, err
		}
	}
	return len(entries), nil
}

func toConnector(id, name, desc, icon, kind, cat string, requiresAuth, enabled, authorized bool, tools json.RawMessage) Connector {
	c := Connector{ID: id, Name: name, Description: desc, IconURL: icon, Kind: kind, Category: cat,
		RequiresAuth: requiresAuth, Enabled: enabled, Authorized: authorized, Tools: []Tool{}}
	_ = json.Unmarshal(tools, &c.Tools)
	return c
}

func (s *Service) List(ctx context.Context, userID uuid.UUID) ([]Connector, error) {
	rows, err := s.Q.ListConnectorsForUser(ctx, userID)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	out := make([]Connector, 0, len(rows))
	for _, r := range rows {
		out = append(out, toConnector(r.ID, r.Name, r.Description, r.IconUrl, r.Kind, r.Category, r.RequiresAuth, r.Enabled, r.Authorized, r.Tools))
	}
	return out, nil
}

func (s *Service) Get(ctx context.Context, userID uuid.UUID, id string) (Connector, error) {
	r, err := s.Q.GetConnectorForUser(ctx, db.GetConnectorForUserParams{UserID: userID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Connector{}, apperr.NotFound("连接器不存在")
	}
	if err != nil {
		return Connector{}, apperr.Internal(err)
	}
	return toConnector(r.ID, r.Name, r.Description, r.IconUrl, r.Kind, r.Category, r.RequiresAuth, r.Enabled, r.Authorized, r.Tools), nil
}

func (s *Service) SetEnabled(ctx context.Context, userID uuid.UUID, id string, enabled bool) (Connector, error) {
	c, err := s.Get(ctx, userID, id)
	if err != nil {
		return c, err
	}
	if enabled && c.RequiresAuth && !c.Authorized {
		return c, apperr.New(409, "auth_required", "请先授权「"+c.Name+"」", false)
	}
	if err := s.Q.SetUserConnector(ctx, db.SetUserConnectorParams{UserID: userID, ConnectorID: id, Enabled: enabled}); err != nil {
		return c, apperr.Internal(err)
	}
	c.Enabled = enabled
	return c, nil
}

func (s *Service) Revoke(ctx context.Context, userID uuid.UUID, id string) error {
	if _, err := s.Get(ctx, userID, id); err != nil {
		return err
	}
	if err := s.Q.DeleteCredential(ctx, db.DeleteCredentialParams{UserID: userID, ConnectorID: id}); err != nil {
		return apperr.Internal(err)
	}
	if err := s.Q.SetUserConnector(ctx, db.SetUserConnectorParams{UserID: userID, ConnectorID: id, Enabled: false}); err != nil {
		return apperr.Internal(err)
	}
	return nil
}
