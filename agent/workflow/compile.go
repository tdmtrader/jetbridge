package workflow

import (
	"fmt"
	"strings"
)

// Compile validates a source manifest and compiles it into a
// self-contained Config (design 2026-07-17 §3): workflow.yml is parsed
// and grammar-validated, prompt_files / system_prompt_file references
// are inlined, context paths are resolved into ContextFiles, and every
// referenced skill's tree is collected into SkillFiles. The render path
// consumes only the compiled Config — never the manifest. Unreferenced
// files (a README, notes) are allowed: they are source, they are
// hashed, and an edit to them correctly mints a new version.
func Compile(m Manifest) (*Config, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	raw, ok := m["workflow.yml"]
	if !ok {
		return nil, fmt.Errorf("workflow: manifest has no workflow.yml")
	}
	cfg, err := Parse([]byte(raw))
	if err != nil {
		return nil, err
	}

	for key, path := range cfg.PromptFiles {
		content, ok := m[path]
		if !ok {
			return nil, fmt.Errorf("workflow: prompt_files %q: %q is not in the manifest", key, path)
		}
		if err := validatePromptTemplate(key, content); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if cfg.Prompts == nil {
			cfg.Prompts = map[string]string{}
		}
		cfg.Prompts[key] = content
	}

	if cfg.SystemPromptFile != "" {
		content, ok := m[cfg.SystemPromptFile]
		if !ok {
			return nil, fmt.Errorf("workflow: system_prompt_file %q is not in the manifest", cfg.SystemPromptFile)
		}
		cfg.SystemPrompt = content
	}

	skillNames := append([]string{}, cfg.Skills...)
	contextPaths := append([]string{}, cfg.Context...)
	for i := range cfg.Steps {
		s := &cfg.Steps[i]
		if s.SystemPromptFile != "" {
			content, ok := m[s.SystemPromptFile]
			if !ok {
				return nil, fmt.Errorf("workflow: agent step %q: system_prompt_file %q is not in the manifest", s.Agent, s.SystemPromptFile)
			}
			s.SystemPrompt = content
		}
		skillNames = append(skillNames, s.Skills...)
		contextPaths = append(contextPaths, s.Context...)
	}

	for _, path := range contextPaths {
		content, ok := m[path]
		if !ok {
			return nil, fmt.Errorf("workflow: context file %q is not in the manifest", path)
		}
		if cfg.ContextFiles == nil {
			cfg.ContextFiles = map[string]string{}
		}
		cfg.ContextFiles[path] = content
	}

	for _, name := range skillNames {
		prefix := "skills/" + name + "/"
		if _, ok := m[prefix+"SKILL.md"]; !ok {
			return nil, fmt.Errorf("workflow: skill %q: %sSKILL.md is not in the manifest", name, prefix)
		}
		for path, content := range m {
			if strings.HasPrefix(path, prefix) {
				if cfg.SkillFiles == nil {
					cfg.SkillFiles = map[string]string{}
				}
				cfg.SkillFiles[path] = content
			}
		}
	}

	return cfg, nil
}
