package ai

import (
	"embed"
	"fmt"
	"strings"
	"text/template"
)

// promptFS holds the prompt templates.
//
// They are files rather than string literals so they can be read, reviewed,
// and diffed as prose. A prompt is the specification of an AI feature's
// behaviour, and burying one inside Go source makes it invisible to everyone
// who is not reading the code.
//
//go:embed prompts/*.md
var promptFS embed.FS

// Prompt names, matching the files in prompts/.
const (
	PromptSystem           = "system"
	PromptAlertContext     = "alert_context"
	PromptEventBrief       = "event_brief"
	PromptMorningBrief     = "morning_brief"
	PromptNewsDigest       = "news_digest"
	PromptExplainMove      = "explain_move"
	PromptOutlook          = "outlook"
	PromptEventClassify    = "event_classify"
	PromptDeepResearch     = "deep_research"
	PromptResearchFollowup = "research_followup"
	PromptSymbolDebrief    = "symbol_debrief"
)

// templates are parsed once at startup. A malformed template is a programming
// error and panics here rather than failing at 08:30 IST inside a cron job.
var templates = func() map[string]*template.Template {
	out := map[string]*template.Template{}
	entries, err := promptFS.ReadDir("prompts")
	if err != nil {
		panic(fmt.Sprintf("ai: prompts directory is missing: %v", err))
	}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".md")
		body, err := promptFS.ReadFile("prompts/" + e.Name())
		if err != nil {
			panic(fmt.Sprintf("ai: could not read prompt %s: %v", e.Name(), err))
		}
		tpl, err := template.New(name).Parse(string(body))
		if err != nil {
			panic(fmt.Sprintf("ai: prompt %s does not parse: %v", e.Name(), err))
		}
		out[name] = tpl
	}
	return out
}()

// RenderPrompt fills a template with data.
func RenderPrompt(name string, data any) (string, error) {
	tpl, ok := templates[name]
	if !ok {
		return "", fmt.Errorf("ai: no prompt named %q", name)
	}
	var b strings.Builder
	if err := tpl.Execute(&b, data); err != nil {
		return "", fmt.Errorf("ai: render prompt %s: %w", name, err)
	}
	return strings.TrimSpace(b.String()), nil
}

// SystemMessage is the shared system prompt every feature sends.
func SystemMessage() Message {
	text, err := RenderPrompt(PromptSystem, nil)
	if err != nil {
		// Unreachable: the template is embedded and parsed at startup.
		panic(err)
	}
	return Message{Role: RoleSystem, Content: text}
}

// UserPrompt renders a template into a user message.
func UserPrompt(name string, data any) (Message, error) {
	text, err := RenderPrompt(name, data)
	if err != nil {
		return Message{}, err
	}
	return Message{Role: RoleUser, Content: text}, nil
}
