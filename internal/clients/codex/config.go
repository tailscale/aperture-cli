package codex

import (
	"strconv"
)

const (
	apertureModelProvider = "tailscale_aperture_cli"
	apertureAPIKeyEnv     = "APERTURE_CODEX_API_KEY"
)

// apertureLaunchConfig returns the CLI overrides and environment needed to
// route Codex through Aperture. The CLI overrides take precedence over the
// user's Codex configuration without replacing CODEX_HOME or rewriting any of
// its files.
func apertureLaunchConfig(apertureHost, modelCatalogPath string) ([]string, map[string]string) {
	provider := "{ name = \"Aperture\", base_url = " + strconv.Quote(apertureHost+"/v1") +
		", env_key = \"" + apertureAPIKeyEnv + "\", supports_websockets = false }"
	args := []string{
		"--config", "model_provider=" + strconv.Quote(apertureModelProvider),
		"--config", "model_providers." + apertureModelProvider + "=" + provider,
	}
	if modelCatalogPath != "" {
		args = append(args, "--config", "model_catalog_json="+strconv.Quote(modelCatalogPath))
	}
	env := map[string]string{
		apertureAPIKeyEnv: "not-needed",
	}
	return args, env
}
