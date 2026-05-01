// Package profiles is the vestigial home of the Claude Desktop (Claude
// Cowork) support. Every other agent has been ported to internal/clients;
// this package stays until Claude Desktop is ported too. It exposes a
// lightweight clients.Client adapter via Client() so the rest of the app
// sees one unified registry.
package profiles

// BackendType identifies the upstream Claude Desktop routes to.
type BackendType string

const (
	// BackendAnthropic is the only backend Claude Desktop supports.
	BackendAnthropic BackendType = "anthropic"
)

// Backend is a selectable upstream destination. Kept for Claude Desktop's
// internal bookkeeping; not exposed outside this package.
type Backend struct {
	Type        BackendType
	DisplayName string
}
