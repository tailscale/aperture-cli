package pi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tailscale/aperture-cli/internal/config"
)

const testHost = "http://ai.example.com"

// isolateConfigDir points config.ClientConfigDir at a temp directory so tests
// never write into the developer's real ~/.config.
func isolateConfigDir(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))
}

func backendByIDOrFatal(t *testing.T, id string) backend {
	t.Helper()
	b, ok := backendByID(id)
	if !ok {
		t.Fatalf("no backend with id %q", id)
	}
	return b
}

func TestBackendBaseURL(t *testing.T) {
	vertexPath := "/v1/projects/_aperture_auto_vertex_project_id_/locations/_aperture_auto_vertex_region_/publishers/google"
	cases := []struct {
		backendID string
		want      string
	}{
		// Pi appends /v1/messages itself, so Anthropic takes the bare host.
		{"anthropic", testHost},
		{"openai_chat", testHost + "/v1"},
		{"openai_responses", testHost + "/v1"},
		{"vertex", testHost + vertexPath},
	}
	for _, tc := range cases {
		t.Run(tc.backendID, func(t *testing.T) {
			b := backendByIDOrFatal(t, tc.backendID)
			if got := b.baseURL(testHost); got != tc.want {
				t.Errorf("baseURL = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBackendBaseURL_TrimsTrailingSlash guards the routing bug this would
// otherwise cause: pi hangs on a doubled slash rather than following the
// endpoint's redirect.
func TestBackendBaseURL_TrimsTrailingSlash(t *testing.T) {
	for _, b := range backends {
		t.Run(b.id, func(t *testing.T) {
			got := b.baseURL(testHost + "/")
			if strings.Contains(got, "//v1") || strings.HasSuffix(got, "//") {
				t.Errorf("baseURL = %q, contains a doubled slash", got)
			}
			if want := b.baseURL(testHost); got != want {
				t.Errorf("baseURL with trailing slash = %q, want %q", got, want)
			}
		})
	}
}

func TestBuildProvider(t *testing.T) {
	p := config.ProviderInfo{
		ID:            "openai-api",
		Name:          "OpenAI",
		Models:        []string{"gpt-5", "gpt-5-mini"},
		Compatibility: map[string]bool{"openai_responses": true},
	}
	b := backendByIDOrFatal(t, "openai_responses")
	prov := buildProvider(testHost, p, b)

	if prov.API != "openai-responses" {
		t.Errorf("api = %q, want openai-responses", prov.API)
	}
	if prov.BaseURL != testHost+"/v1" {
		t.Errorf("baseUrl = %q, want %q", prov.BaseURL, testHost+"/v1")
	}
	// Pi drops a provider whose models carry no apiKey: it loads the file but
	// never offers the models in /model or --list-models.
	if prov.APIKey != "not-needed" {
		t.Errorf("apiKey = %q, want not-needed", prov.APIKey)
	}
	if prov.Name != "Aperture (openai-api)" {
		t.Errorf("name = %q, want Aperture (openai-api)", prov.Name)
	}
	if len(prov.Models) != 2 {
		t.Fatalf("models len = %d, want 2", len(prov.Models))
	}
	// Model IDs must be bare: pi sends id verbatim as the wire model name.
	for i, want := range []string{"gpt-5", "gpt-5-mini"} {
		if prov.Models[i].ID != want {
			t.Errorf("models[%d].id = %q, want %q", i, prov.Models[i].ID, want)
		}
	}
	// input must be non-empty or pi's --list-models dereferences nil.
	for i, m := range prov.Models {
		if len(m.Input) == 0 {
			t.Errorf("models[%d].input is empty; pi crashes on a nil input list", i)
		}
		// A model registered from an extension gets no default token limits,
		// unlike one declared in models.json. Leaving these zero sends
		// max_tokens: null and the request fails at the provider.
		if m.MaxTokens <= 0 {
			t.Errorf("models[%d].maxTokens = %d; must be positive or the request is rejected", i, m.MaxTokens)
		}
		if m.ContextWindow <= 0 {
			t.Errorf("models[%d].contextWindow = %d; must be positive", i, m.ContextWindow)
		}
	}
}

// TestExtensionSource_NoNullFields guards the failure that end-to-end testing
// found: a model field omitted from the generated JSON reaches the provider as
// a literal null rather than falling back to a pi default.
func TestExtensionSource_NoNullFields(t *testing.T) {
	p := config.ProviderInfo{ID: "anthropic", Models: []string{"claude-sonnet-4-5"}}
	src, err := extensionSource(p.ID, buildProvider(testHost, p, backendByIDOrFatal(t, "anthropic")))
	if err != nil {
		t.Fatalf("extensionSource: %v", err)
	}
	if strings.Contains(src, "null") {
		t.Errorf("generated extension contains a null value:\n%s", src)
	}
	for _, field := range []string{"maxTokens", "contextWindow", "input", "apiKey", "baseUrl", "api"} {
		if !strings.Contains(src, `"`+field+`"`) {
			t.Errorf("generated extension omits %q", field)
		}
	}
}

func TestBuildProvider_NoModels(t *testing.T) {
	p := config.ProviderInfo{ID: "empty", Compatibility: map[string]bool{"openai_chat": true}}
	prov := buildProvider(testHost, p, backendByIDOrFatal(t, "openai_chat"))
	if len(prov.Models) != 0 {
		t.Errorf("models len = %d, want 0", len(prov.Models))
	}
}

func TestExtensionSource(t *testing.T) {
	p := config.ProviderInfo{
		ID:     "anthropic",
		Models: []string{"claude-sonnet-4-5"},
	}
	b := backendByIDOrFatal(t, "anthropic")
	src, err := extensionSource(p.ID, buildProvider(testHost, p, b))
	if err != nil {
		t.Fatalf("extensionSource: %v", err)
	}

	// The extension must default-export a function that registers the
	// provider; anything else and pi loads the file and does nothing.
	if !strings.Contains(src, "export default function") {
		t.Error("source has no default-exported function")
	}
	if !strings.Contains(src, "pi.registerProvider(") {
		t.Error("source never calls pi.registerProvider")
	}
	// The registered ID must be namespaced so it cannot merge into pi's own
	// built-in "anthropic" provider and retarget the user's models.
	if !strings.Contains(src, `"aperture-anthropic"`) {
		t.Errorf("source does not register the namespaced provider id:\n%s", src)
	}
	if strings.Contains(src, `registerProvider("anthropic"`) {
		t.Error("source registers the bare provider id, which would override pi's built-in provider")
	}
}

func TestPiProviderIDAndModelRef(t *testing.T) {
	if got := piProviderID("openai-api"); got != "aperture-openai-api" {
		t.Errorf("piProviderID = %q, want aperture-openai-api", got)
	}
	// The model arrives fully-qualified from the menu; the reference pi wants
	// is the namespaced provider plus the bare model ID.
	if got := piModelRef("openai-api", "openai-api/gpt-5"); got != "aperture-openai-api/gpt-5" {
		t.Errorf("piModelRef = %q, want aperture-openai-api/gpt-5", got)
	}
	if got := piModelRef("vertex", "gemini-2.5-pro"); got != "aperture-vertex/gemini-2.5-pro" {
		t.Errorf("piModelRef = %q, want aperture-vertex/gemini-2.5-pro", got)
	}
}

func TestWriteProviderExtension(t *testing.T) {
	isolateConfigDir(t)

	p := config.ProviderInfo{
		ID:            "anthropic",
		Name:          "Anthropic",
		Models:        []string{"claude-sonnet-4-5"},
		Compatibility: map[string]bool{"anthropic_messages": true},
	}
	b := backendByIDOrFatal(t, "anthropic")

	path, cleanup, err := writeProviderExtension(testHost, p, b)
	if err != nil {
		t.Fatalf("writeProviderExtension: %v", err)
	}

	// Pi resolves -e by extension, so a non-.js path is never loaded.
	if filepath.Ext(path) != ".js" {
		t.Errorf("path = %q, want a .js file", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("extension unreadable: %v", err)
	}
	if !strings.Contains(string(data), testHost) {
		t.Errorf("extension does not contain the aperture host:\n%s", data)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %o, want 600", perm)
	}

	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("extension file still exists after cleanup")
	}
}

func TestWriteProviderExtension_UniquePaths(t *testing.T) {
	isolateConfigDir(t)

	p := config.ProviderInfo{ID: "openai-api", Models: []string{"gpt-5"}}
	b := backendByIDOrFatal(t, "openai_responses")
	path1, cleanup1, err := writeProviderExtension(testHost, p, b)
	if err != nil {
		t.Fatalf("first writeProviderExtension: %v", err)
	}
	defer cleanup1()
	path2, cleanup2, err := writeProviderExtension(testHost, p, b)
	if err != nil {
		t.Fatalf("second writeProviderExtension: %v", err)
	}
	defer cleanup2()

	if path1 == path2 {
		t.Fatalf("concurrent extensions use the same path %q", path1)
	}
	cleanup1()
	if _, err := os.Stat(path2); err != nil {
		t.Errorf("cleaning up the first extension affected the second: %v", err)
	}
}

// TestWriteProviderExtension_EmbeddedJSONIsValid checks the generated file's
// provider config parses as JSON, which is what catches an unescaped value
// silently producing a broken extension.
func TestWriteProviderExtension_EmbeddedJSONIsValid(t *testing.T) {
	isolateConfigDir(t)

	p := config.ProviderInfo{
		ID:            "openai-api",
		Models:        []string{"gpt-5"},
		Compatibility: map[string]bool{"openai_responses": true},
	}
	b := backendByIDOrFatal(t, "openai_responses")
	path, cleanup, err := writeProviderExtension(testHost, p, b)
	if err != nil {
		t.Fatalf("writeProviderExtension: %v", err)
	}
	defer cleanup()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)

	// The config object is the second argument to registerProvider: it starts
	// at the ", {" that follows the provider ID and ends at the closing ");".
	start := strings.Index(src, ", {")
	end := strings.LastIndex(src, ");")
	if start < 0 || end <= start {
		t.Fatalf("no provider config object found in:\n%s", src)
	}
	blob := src[start+2 : end]

	var got piProvider
	if err := json.Unmarshal([]byte(blob), &got); err != nil {
		t.Fatalf("embedded provider config is not valid JSON: %v\n%s", err, blob)
	}
	if got.API != "openai-responses" {
		t.Errorf("api = %q, want openai-responses", got.API)
	}
	if got.BaseURL != testHost+"/v1" {
		t.Errorf("baseUrl = %q, want %q", got.BaseURL, testHost+"/v1")
	}
	if len(got.Models) != 1 || got.Models[0].ID != "gpt-5" {
		t.Errorf("models = %+v, want one gpt-5 entry", got.Models)
	}
}

// TestEveryBackendEmitsALoadableExtension is the offline form of the live
// end-to-end run: every backend in the table was driven against a real
// Aperture endpoint with both a text request and a tool-calling request, and
// all four returned successfully. What that run actually exercised is the
// artifact each backend produces, so this asserts the same properties on every
// backend without needing an endpoint at test time.
//
// It is a loop over backends rather than fixed cases on purpose: a new
// protocol added to the table is covered the moment it is added, instead of
// shipping untested because nobody remembered to add a case.
func TestEveryBackendEmitsALoadableExtension(t *testing.T) {
	for _, b := range backends {
		t.Run(b.id, func(t *testing.T) {
			p := config.ProviderInfo{
				ID:     "prov",
				Name:   "Prov",
				Models: []string{"model-a"},
			}
			src, err := extensionSource(p.ID, buildProvider(testHost, p, b))
			if err != nil {
				t.Fatalf("extensionSource: %v", err)
			}

			// A null anywhere is the failure mode the live run was built to
			// catch: pi passes the value through and the provider rejects it.
			if strings.Contains(src, "null") {
				t.Errorf("extension contains a null value:\n%s", src)
			}
			// The base URL must never carry a doubled slash; pi hangs rather
			// than following the endpoint's redirect.
			if strings.Contains(b.baseURL(testHost), "//v1") {
				t.Errorf("baseURL = %q, contains a doubled slash", b.baseURL(testHost))
			}

			start := strings.Index(src, ", {")
			end := strings.LastIndex(src, ");")
			if start < 0 || end <= start {
				t.Fatalf("no provider config object found in:\n%s", src)
			}
			var got piProvider
			if err := json.Unmarshal([]byte(src[start+2:end]), &got); err != nil {
				t.Fatalf("embedded provider config is not valid JSON: %v", err)
			}

			// The api value is what selects pi's wire protocol; a wrong or
			// empty one routes the request at the wrong endpoint shape.
			if got.API != b.api {
				t.Errorf("api = %q, want %q", got.API, b.api)
			}
			if got.BaseURL != b.baseURL(testHost) {
				t.Errorf("baseUrl = %q, want %q", got.BaseURL, b.baseURL(testHost))
			}
			if got.APIKey == "" {
				t.Error("apiKey is empty; pi drops a provider with no key")
			}
			if len(got.Models) != 1 {
				t.Fatalf("models len = %d, want 1", len(got.Models))
			}
			// Every field the tool-calling path needs must be populated.
			m := got.Models[0]
			if m.ID != "model-a" {
				t.Errorf("models[0].id = %q, want model-a", m.ID)
			}
			if len(m.Input) == 0 {
				t.Error("models[0].input is empty; pi crashes on a nil input list")
			}
			if m.MaxTokens <= 0 {
				t.Errorf("models[0].maxTokens = %d; a zero value is sent as null and rejected", m.MaxTokens)
			}
			if m.ContextWindow <= 0 {
				t.Errorf("models[0].contextWindow = %d; must be positive", m.ContextWindow)
			}

			// The argv pi is launched with must name the extension and a
			// model reference carrying no provider/ prefix on the model half.
			args := buildArgs("/tmp/ext.js", p.ID, "prov/model-a")
			want := []string{"-e", "/tmp/ext.js", "--model", "aperture-prov/model-a"}
			if !slices.Equal(args, want) {
				t.Errorf("buildArgs = %v, want %v", args, want)
			}
		})
	}
}

func TestBuildArgs(t *testing.T) {
	t.Run("with_model", func(t *testing.T) {
		got := buildArgs("/tmp/ext.js", "openai-api", "openai-api/gpt-5")
		want := []string{"-e", "/tmp/ext.js", "--model", "aperture-openai-api/gpt-5"}
		if !slices.Equal(got, want) {
			t.Errorf("buildArgs = %v, want %v", got, want)
		}
	})
	t.Run("no_model_omits_flag", func(t *testing.T) {
		got := buildArgs("/tmp/ext.js", "openai-api", "")
		want := []string{"-e", "/tmp/ext.js"}
		if !slices.Equal(got, want) {
			t.Errorf("buildArgs = %v, want %v", got, want)
		}
	})
	t.Run("never_sets_a_yolo_flag", func(t *testing.T) {
		// Pi has no permission prompts, so nothing here should look like an
		// approval bypass. --approve is project-file trust, not tool approval.
		for _, a := range buildArgs("/tmp/ext.js", "p", "p/m") {
			if a == "--approve" || a == "-a" || a == "--yolo" {
				t.Errorf("buildArgs included %q", a)
			}
		}
	})
}

func TestBackendsFor(t *testing.T) {
	cases := []struct {
		name   string
		compat map[string]bool
		want   []string
	}{
		{
			name:   "openai_both_ordered_responses_first",
			compat: map[string]bool{"openai_chat": true, "openai_responses": true},
			want:   []string{"openai_responses", "openai_chat"},
		},
		{
			name:   "anthropic_only",
			compat: map[string]bool{"anthropic_messages": true},
			want:   []string{"anthropic"},
		},
		{
			name:   "vertex_via_generate_content",
			compat: map[string]bool{"google_generate_content": true},
			want:   []string{"vertex"},
		},
		{
			name:   "vertex_via_raw_predict",
			compat: map[string]bool{"google_raw_predict": true},
			want:   []string{"vertex"},
		},
		{
			name:   "vertex_not_duplicated_when_both_keys_set",
			compat: map[string]bool{"google_generate_content": true, "google_raw_predict": true},
			want:   []string{"vertex"},
		},
		{
			// Pi's bedrock API type fails at request time, so it is not offered.
			name:   "bedrock_unsupported",
			compat: map[string]bool{"bedrock_converse": true, "bedrock_model_invoke": true},
			want:   nil,
		},
		{
			name:   "unknown_key",
			compat: map[string]bool{"something_else": true},
			want:   nil,
		},
		{
			name:   "all_four",
			compat: map[string]bool{"openai_chat": true, "openai_responses": true, "anthropic_messages": true, "google_raw_predict": true},
			want:   []string{"openai_responses", "anthropic", "openai_chat", "vertex"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bs := backendsFor(config.ProviderInfo{Compatibility: tc.compat})
			got := make([]string, len(bs))
			for i, b := range bs {
				got[i] = b.id
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("backendsFor = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCompatibleProviders(t *testing.T) {
	provs := []config.ProviderInfo{
		{ID: "openai-api", Models: []string{"gpt-5"}, Compatibility: map[string]bool{"openai_responses": true}},
		{ID: "bedrock", Models: []string{"model"}, Compatibility: map[string]bool{"bedrock_converse": true}},
		{ID: "anthropic", Models: []string{"claude"}, Compatibility: map[string]bool{"anthropic_messages": true}},
		{ID: "empty", Compatibility: map[string]bool{"openai_responses": true}},
		{ID: "none", Models: []string{"model"}, Compatibility: map[string]bool{"something_else": true}},
	}
	got := compatibleProviders(provs)
	ids := make([]string, len(got))
	for i, p := range got {
		ids[i] = p.ID
	}
	// bedrock and none have no supported protocol; empty has no routable model.
	if want := []string{"openai-api", "anthropic"}; !slices.Equal(ids, want) {
		t.Errorf("compatibleProviders = %v, want %v", ids, want)
	}
}

func TestBackendByID(t *testing.T) {
	if b, ok := backendByID("vertex"); !ok || b.api != "google-generative-ai" {
		t.Errorf("backendByID(vertex) = %+v, %v", b, ok)
	}
	if _, ok := backendByID("bedrock"); ok {
		t.Error("backendByID(bedrock) should not resolve")
	}
	if _, ok := backendByID(""); ok {
		t.Error("backendByID(\"\") should not resolve")
	}
}

// TestResolveReplay covers every staleness path Replay depends on. It calls
// resolveReplay rather than Replay on purpose: Replay returns nil at
// !IsInstalled() before reaching any of these checks, so a test driving
// Replay would pass on CI even with the checks deleted.
func TestResolveReplay(t *testing.T) {
	liveProvider := config.ProviderInfo{
		ID:            "openai-api",
		Name:          "OpenAI",
		Models:        []string{"gpt-5"},
		Compatibility: map[string]bool{"openai_responses": true, "openai_chat": true},
	}
	base := config.LaunchState{
		LastClientName:  name,
		LastBackendType: "openai_responses",
		LastProviderID:  "openai-api",
		LastModel:       "openai-api/gpt-5",
	}

	t.Run("replayable", func(t *testing.T) {
		g := &config.Global{Providers: []config.ProviderInfo{liveProvider}, LastLaunch: base}
		prov, b, model, ok := resolveReplay(g)
		if !ok {
			t.Fatal("resolveReplay = not ok, want a replayable launch")
		}
		if prov.ID != "openai-api" {
			t.Errorf("provider = %q, want openai-api", prov.ID)
		}
		if b.id != "openai_responses" {
			t.Errorf("backend = %q, want openai_responses", b.id)
		}
		if model != "openai-api/gpt-5" {
			t.Errorf("model = %q, want openai-api/gpt-5", model)
		}
	})

	t.Run("empty_model_is_replayable", func(t *testing.T) {
		ls := base
		ls.LastModel = ""
		g := &config.Global{Providers: []config.ProviderInfo{liveProvider}, LastLaunch: ls}
		if _, _, model, ok := resolveReplay(g); !ok || model != "" {
			t.Errorf("resolveReplay = %q, %v; want \"\", true", model, ok)
		}
	})

	stale := []struct {
		name      string
		providers []config.ProviderInfo
		mutate    func(*config.LaunchState)
	}{
		{
			name:      "another_client_owns_the_record",
			providers: []config.ProviderInfo{liveProvider},
			mutate:    func(ls *config.LaunchState) { ls.LastClientName = "Codex" },
		},
		{
			name:      "provider_gone_from_endpoint",
			providers: []config.ProviderInfo{liveProvider},
			mutate:    func(ls *config.LaunchState) { ls.LastProviderID = "removed" },
		},
		{
			name:      "backend_id_no_longer_offered",
			providers: []config.ProviderInfo{liveProvider},
			mutate:    func(ls *config.LaunchState) { ls.LastBackendType = "bedrock" },
		},
		{
			name:      "backend_id_empty",
			providers: []config.ProviderInfo{liveProvider},
			mutate:    func(ls *config.LaunchState) { ls.LastBackendType = "" },
		},
		{
			// The provider still exists but dropped the protocol we recorded.
			name: "provider_dropped_the_protocol",
			providers: []config.ProviderInfo{{
				ID:            "openai-api",
				Models:        []string{"gpt-5"},
				Compatibility: map[string]bool{"openai_chat": true},
			}},
			mutate: func(ls *config.LaunchState) {},
		},
		{
			name:      "model_no_longer_listed",
			providers: []config.ProviderInfo{liveProvider},
			mutate:    func(ls *config.LaunchState) { ls.LastModel = "openai-api/gpt-4" },
		},
		{
			name: "provider_has_no_models_anymore",
			providers: []config.ProviderInfo{{
				ID:            "openai-api",
				Compatibility: map[string]bool{"openai_responses": true},
			}},
			mutate: func(ls *config.LaunchState) {},
		},
	}
	for _, tc := range stale {
		t.Run(tc.name, func(t *testing.T) {
			ls := base
			tc.mutate(&ls)
			g := &config.Global{Providers: tc.providers, LastLaunch: ls}
			if _, _, _, ok := resolveReplay(g); ok {
				t.Error("resolveReplay = ok, want not replayable")
			}
		})
	}
}

// TestReplayStalenessChecks exercises the decisions Replay makes, directly
// against the unexported helpers. Calling Replay itself would return nil at
// the !IsInstalled() check on any machine without pi, so it would pass even
// if the staleness logic were deleted.
func TestReplayStalenessChecks(t *testing.T) {
	prov := config.ProviderInfo{
		ID:            "openai-api",
		Models:        []string{"gpt-5"},
		Compatibility: map[string]bool{"openai_responses": true},
	}

	b := backendByIDOrFatal(t, "openai_responses")
	if !providerSupports(prov, b) {
		t.Error("provider should support the recorded backend")
	}

	// A backend the provider no longer serves must not replay.
	if providerSupports(prov, backendByIDOrFatal(t, "anthropic")) {
		t.Error("provider without anthropic_messages should not support the anthropic backend")
	}

	// A backend ID that no longer exists in the table must not replay.
	if _, ok := backendByID("openai_completions_legacy"); ok {
		t.Error("an unknown recorded backend id should not resolve")
	}

	if got := fqnModels(prov); !slices.Equal(got, []string{"openai-api/gpt-5"}) {
		t.Errorf("fqnModels = %v, want [openai-api/gpt-5]", got)
	}
	// A recorded model the provider no longer lists is what makes Replay bail.
	if slices.Contains(fqnModels(prov), "openai-api/gpt-4") {
		t.Error("stale model should not be found in the current model list")
	}
}

func TestQuickSelectLabel(t *testing.T) {
	g := &config.Global{
		Providers: []config.ProviderInfo{
			{ID: "openai-api", Name: "OpenAI", Models: []string{"gpt-5"}},
		},
		LastLaunch: config.LaunchState{
			LastClientName:  name,
			LastBackendType: "openai_responses",
			LastProviderID:  "openai-api",
			LastModel:       "openai-api/gpt-5",
		},
	}
	c := &Client{}
	want := "Pi via OpenAI - OpenAI Responses - openai-api/gpt-5"
	if got := c.QuickSelectLabel(g); got != want {
		t.Errorf("QuickSelectLabel = %q, want %q", got, want)
	}

	// No recorded model: the label stops after the backend.
	g.LastLaunch.LastModel = ""
	if got, want := c.QuickSelectLabel(g), "Pi via OpenAI - OpenAI Responses"; got != want {
		t.Errorf("QuickSelectLabel = %q, want %q", got, want)
	}
}

func TestFqnModels(t *testing.T) {
	p := config.ProviderInfo{ID: "openai-api", Models: []string{"gpt-5", "gpt-5-mini"}}
	want := []string{"openai-api/gpt-5", "openai-api/gpt-5-mini"}
	if got := fqnModels(p); !slices.Equal(got, want) {
		t.Errorf("fqnModels = %v, want %v", got, want)
	}
}

func TestStripProviderPrefix(t *testing.T) {
	cases := map[string]string{
		"openai-api/gpt-5":          "gpt-5",
		"anthropic/claude-sonnet-4": "claude-sonnet-4",
		"bare-model":                "bare-model",
		"provider/nested/model":     "nested/model",
	}
	for in, want := range cases {
		if got := stripProviderPrefix(in); got != want {
			t.Errorf("stripProviderPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInstallUninstall(t *testing.T) {
	c := &Client{}
	install := c.Install(&config.Global{})
	if install.Hint != installCmd {
		t.Errorf("Install.Hint = %q, want %q", install.Hint, installCmd)
	}
	if install.Run == nil {
		t.Error("Install.Run is nil")
	}

	uninstall := c.Uninstall()
	if uninstall.Hint != uninstallCmd {
		t.Errorf("Uninstall.Hint = %q, want %q", uninstall.Hint, uninstallCmd)
	}
	if uninstall.Run == nil {
		t.Error("Uninstall.Run is nil")
	}
}

func TestIdentity(t *testing.T) {
	c := &Client{}
	if c.Name() != "Pi" {
		t.Errorf("Name = %q, want Pi", c.Name())
	}
	if c.BinaryName() != "pi" {
		t.Errorf("BinaryName = %q, want pi", c.BinaryName())
	}
	// CommonPaths must be full paths to the binary, not directories:
	// FindBinary stats each entry directly.
	for _, p := range c.CommonPaths() {
		if filepath.Base(p) != "pi" {
			t.Errorf("CommonPaths entry %q does not end in the binary name", p)
		}
		if !filepath.IsAbs(p) {
			t.Errorf("CommonPaths entry %q is not absolute", p)
		}
	}
}
