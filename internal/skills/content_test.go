package skills

import (
	"strings"
	"testing"
)

// params is a representative launcher state.
func params() ContentParams {
	return ContentParams{
		Version:   "B18",
		Endpoint:  "https://aperture.example.ts.net",
		ConfigDir: "/home/u/.config/aperture",
		Agents: []AgentInfo{
			{Name: "Claude Code", Binary: "claude", InstallHint: "curl -fsSL https://claude.ai/install.sh | bash"},
			{Name: "OpenCode", Binary: "opencode", InstallHint: "curl -fsSL https://opencode.ai/install | bash"},
			{Name: "Claude Cowork", Binary: "", InstallHint: ""},
		},
	}
}

// sample renders every skill joined together, for topic coverage checks that
// do not care which skill carries a given fact.
func sample() string {
	var b strings.Builder
	for _, s := range Content(params()) {
		b.WriteString(s.Body)
		b.WriteString("\n")
	}
	return b.String()
}

// bySkill renders the skills keyed by name.
func bySkill(p ContentParams) map[string]string {
	out := map[string]string{}
	for _, s := range Content(p) {
		out[s.Name] = s.Body
	}
	return out
}

// TestContentCoversDocumentedTopics pins the skill against the published
// Aperture CLI documentation. Each entry is a topic the docs cover that an
// agent reading this skill would otherwise have to look up. Dropping one is a
// coverage regression, so this test names the topic rather than the wording.
func TestContentCoversDocumentedTopics(t *testing.T) {
	got := sample()

	topics := map[string][]string{
		"install command":     {"go install github.com/tailscale/aperture-cli/cmd/aperture@latest"},
		"go version":          {"Go 1.26+"},
		"version flag":        {"-version"},
		"debug flag":          {"-debug"},
		"skills subcommand":   {"aperture skills install"},
		"navigation keys":     {"`j`/`k`", "`h`/`l`"},
		"settings key":        {"| `s` | Settings |"},
		"install agents key":  {"| `i` | Install agents |"},
		"quit keys":           {"| `q` | Quit |", "Ctrl+C"},
		"last used slot":      {"`[0]`"},
		"single option menus": {"auto-selected"},
		"endpoint management": {"Aperture endpoints", "Direct", "Bridge"},
		"bridges":             {"embedded Tailscale node", "admin console"},
		"ts-unplug":           {"ts-unplug", "-port", "-dir"},
		"yolo mode":           {"--dangerously-skip-permissions", "--yolo", "--dangerously-bypass-approvals-and-sandbox"},
		"agent install":       {"Installing and removing agents"},
		"provider types":      {"Bedrock", "Vertex", "OpenAI Responses"},
		"path suffixes":       {"/bedrock"},
		"model prefix strip":  {"anthropic/claude-sonnet-5"},
		"cowork platform":     {"macOS and\nWindows only"},
		"cowork https":        {"rewrites `http://` to"},
		"passthrough caveat":  {"ANTHROPIC_AUTH_TOKEN=-"},
		"state files":         {"settings.json", "launcher.json", "bridges/<id>/"},
		"binary lookup":       {"~/.local/bin", "~/.npm-global/bin"},
		"claude settings":     {"~/.claude/settings.json"},
		"gemini fqdn":         {"HTTPS FQDN"},
		"one agent limit":     {"One agent per session"},
		"connectors excluded": {"Aperture dashboard"},
	}

	for topic, needles := range topics {
		for _, needle := range needles {
			if !strings.Contains(got, needle) {
				t.Errorf("skill is missing coverage of %s: no %q", topic, needle)
			}
		}
	}
}

func TestContentListsAgentsFromTheRegistry(t *testing.T) {
	got := sample()

	for _, want := range []string{"Claude Code", "`claude`", "OpenCode", "`opencode`"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q from the agent table", want)
		}
	}

	// An agent with no binary (a desktop app) must still render a row rather
	// than an empty cell pair.
	if !strings.Contains(got, "| Claude Cowork | — | — |") {
		t.Error("an agent without a binary should render em dashes")
	}
}

func TestContentEscapesPipesInTableCells(t *testing.T) {
	got := sample()

	// "curl ... | bash" must not end the table cell early.
	if !strings.Contains(got, `curl -fsSL https://claude.ai/install.sh \| bash`) {
		t.Error("a pipe inside an install hint must be escaped for the table")
	}

	// Every row of a table must have the same number of unescaped pipes as
	// that table's header, or a cell has leaked into the next column.
	columns := 0
	for _, line := range strings.Split(got, "\n") {
		if !strings.HasPrefix(line, "|") {
			columns = 0
			continue
		}
		n := strings.Count(line, "|") - strings.Count(line, `\|`)
		if columns == 0 {
			columns = n
			continue
		}
		if n != columns {
			t.Errorf("row has %d unescaped pipes, header had %d: %s", n, columns, line)
		}
	}
}

func TestContentUsesResolvedConfigDir(t *testing.T) {
	got := sample()
	if !strings.Contains(got, "/home/u/.config/aperture") {
		t.Error("a resolved config directory should be named directly")
	}

	// Without one, the skill should fall back to describing each platform.
	fallback := bySkill(ContentParams{})["aperture-troubleshooting"]
	for _, want := range []string{"Library/Application Support", ".config", "LOCALAPPDATA"} {
		if !strings.Contains(fallback, want) {
			t.Errorf("fallback should mention %q", want)
		}
	}
}

func TestContentOmitsEmptyOptionalFields(t *testing.T) {
	var b strings.Builder
	for _, s := range Content(ContentParams{}) {
		b.WriteString(s.Body)
	}
	got := b.String()

	if strings.Contains(got, "Configured endpoint") {
		t.Error("no endpoint configured should not render an endpoint line")
	}
	if strings.Contains(got, "Generated by aperture") {
		t.Error("no version should not render a generated-by line")
	}
	if strings.Contains(got, "## Agents") {
		t.Error("no agents should not render an empty agent table")
	}
}

func TestEverySkillHasFrontmatter(t *testing.T) {
	for _, s := range Content(params()) {
		if !strings.HasPrefix(s.Body, "---\nname: "+s.Name+"\n") {
			t.Errorf("%s: frontmatter must open with its own name", s.Name)
		}
		if strings.Count(s.Body, "---\n") < 2 {
			t.Errorf("%s: frontmatter must be closed", s.Name)
		}
		if !strings.Contains(s.Body, "description: ") {
			t.Errorf("%s: needs a description for skill discovery", s.Name)
		}
	}
}

// TestSkillsSplitByComponent pins which component each skill owns, so a fact
// stays where an agent would look for it.
func TestSkillsSplitByComponent(t *testing.T) {
	skills := bySkill(params())

	want := map[string][]string{
		"aperture":                 {"## Commands", "## Keys", "aperture -debug"},
		"aperture-agents":          {"YOLO mode", "ANTHROPIC_AUTH_TOKEN=-", "Provider types"},
		"aperture-endpoints":       {"Aperture endpoints", "Direct", "Bridge"},
		"aperture-bridges":         {"ts-unplug", "-port", "admin console"},
		"aperture-troubleshooting": {"settings.json", "HTTPS FQDN", "## Limits"},
	}

	for name, needles := range want {
		body, ok := skills[name]
		if !ok {
			t.Errorf("missing skill %q", name)
			continue
		}
		for _, needle := range needles {
			if !strings.Contains(body, needle) {
				t.Errorf("%s should cover %q", name, needle)
			}
		}
	}

	if len(skills) != len(allSkillNames()) {
		t.Errorf("generated %d skills, allSkillNames lists %d", len(skills), len(allSkillNames()))
	}
}

// TestCoreSkillLinksTheOthers keeps the set discoverable: an agent that loads
// the core skill should learn the rest exist.
func TestCoreSkillLinksTheOthers(t *testing.T) {
	core := bySkill(params())["aperture"]
	for _, name := range allSkillNames() {
		if name == "aperture" {
			continue
		}
		if !strings.Contains(core, name) {
			t.Errorf("core skill should mention %q", name)
		}
	}
}
