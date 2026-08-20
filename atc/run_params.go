package atc

type ParamType string

const (
	ParamTypeString         ParamType = "string"
	ParamTypeNumber         ParamType = "number"
	ParamTypeBool           ParamType = "bool"
	ParamTypeEnum           ParamType = "enum"
	MaxRunRetentionKeepLast           = 2147483647
	MaxRunRetentionTTLDays            = 1000000
)

type ParamSchema struct {
	Name        string    `json:"name"`
	Type        ParamType `json:"type"`
	Required    bool      `json:"required,omitempty"`
	Default     any       `json:"default,omitempty"`
	Values      []any     `json:"values,omitempty"`
	Description string    `json:"description,omitempty"`
}

type RunRetentionConfig struct {
	KeepLast *int `json:"keep_last,omitempty"`
	TTLDays  *int `json:"ttl_days,omitempty"`
}
