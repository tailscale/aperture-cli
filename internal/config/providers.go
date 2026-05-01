package config

// ProviderInfo mirrors the JSON response from GET /api/providers.
type ProviderInfo struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	Models        []string        `json:"models"`
	Compatibility map[string]bool `json:"compatibility"`
}

// DisplayName returns the provider's Name, falling back to ID if Name is empty.
func (p ProviderInfo) DisplayName() string {
	if p.Name != "" {
		return p.Name
	}
	return p.ID
}
