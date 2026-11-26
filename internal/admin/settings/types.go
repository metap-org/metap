package settings

import "encoding/json"

// The names and tags are what the OpenAPI document shows, so they stay as they were when these
// lived in package admin.

type SetConfigRequest struct {
	Key   string          `json:"-"`
	Value json.RawMessage `json:"value" required:"false"`
}

type ConfigKeyRequest struct {
	Key string
}

type PlatformConfigItem struct {
	Key               string          `json:"key"`
	Value             json.RawMessage `json:"value"`
	Level             string          `json:"level"`
	TenantOverridable bool            `json:"tenantOverridable"`
}

type PlatformConfigList struct {
	Data []*PlatformConfigItem `json:"data"`
}

type PlatformConfigSet struct {
	Key                string          `json:"key"`
	Value              json.RawMessage `json:"value"`
	AppliesImmediately bool            `json:"appliesImmediately"`
}

type PlatformConfigResult struct {
	Data *PlatformConfigSet `json:"data"`
}

type TenantConfigItem struct {
	Key        string          `json:"key"`
	Value      json.RawMessage `json:"value"`
	Level      string          `json:"level"`
	Overridden bool            `json:"overridden"`
	Public     bool            `json:"public"`
	Secret     bool            `json:"secret"`
}

type TenantConfigList struct {
	Data []*TenantConfigItem `json:"data"`
}

type TenantConfigSet struct {
	Key        string          `json:"key"`
	Value      json.RawMessage `json:"value"`
	Overridden bool            `json:"overridden"`
}

type TenantConfigResult struct {
	Data *TenantConfigSet `json:"data"`
}
