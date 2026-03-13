package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrRoleNotFound is returned when a role prompt file cannot be found.
var ErrRoleNotFound = errors.New("role prompt not found")

type Executor struct {
	Command string   `yaml:"command"`
	Args    []string `yaml:"args"`
}

type Config struct {
	Executor Executor          `yaml:"executor"`
	Models   map[string]string `yaml:"models"`
}

const (
	ExecutorPresetCodex  = "codex"
	ExecutorPresetClaude = "claude"
	ExecutorPresetGemini = "gemini"
)

func ConfigPath(root string) string {
	return filepath.Join(root, ".forgeworld.yml")
}

func LoadLocal(root string) (*Config, error) {
	path := ConfigPath(root)
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.Executor.Command) == "" {
		return nil, errors.New("executor.command es obligatorio")
	}
	for _, k := range []string{"small", "medium", "large"} {
		if strings.TrimSpace(cfg.Models[k]) == "" {
			return nil, fmt.Errorf("models.%s es obligatorio", k)
		}
	}
	return &cfg, nil
}

func Default() *Config {
	return &Config{
		Executor: Executor{
			Command: "echo",
			Args: []string{
				"Configura .forgeworld.yml para invocar tu agente con {{model}} {{prompt}}",
			},
		},
		Models: map[string]string{
			"small":  "model-small",
			"medium": "model-medium",
			"large":  "model-large",
		},
	}
}

func DefaultForExecutorPreset(preset string) (*Config, error) {
	switch strings.ToLower(strings.TrimSpace(preset)) {
	case "":
		return Default(), nil
	case ExecutorPresetCodex:
		return &Config{
			Executor: Executor{
				Command: "bash",
				Args: []string{
					"-lc",
					"codex exec -a never --sandbox workspace-write --model \"{{model}}\" - < \"{{prompt}}\"",
				},
			},
			Models: map[string]string{
				"small":  "gpt-5-mini",
				"medium": "gpt-5",
				"large":  "gpt-5",
			},
		}, nil
	case ExecutorPresetClaude:
		return &Config{
			Executor: Executor{
				Command: "bash",
				Args: []string{
					"-lc",
					"claude -p --permission-mode bypassPermissions --model \"{{model}}\" < \"{{prompt}}\"",
				},
			},
			Models: map[string]string{
				"small":  "haiku",
				"medium": "sonnet",
				"large":  "opus",
			},
		}, nil
	case ExecutorPresetGemini:
		return &Config{
			Executor: Executor{
				Command: "bash",
				Args: []string{
					"-lc",
					"gemini --model \"{{model}}\" < \"{{prompt}}\"",
				},
			},
			Models: map[string]string{
				"small":  "gemini-2.5-flash",
				"medium": "gemini-2.5-pro",
				"large":  "gemini-2.5-pro",
			},
		}, nil
	default:
		return nil, fmt.Errorf("executor invalido %q: use codex|claude|gemini", preset)
	}
}

func SaveDefaultIfMissing(root, preset string) (bool, error) {
	path := ConfigPath(root)
	preset = strings.TrimSpace(preset)

	_, statErr := os.Stat(path)
	exists := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return false, statErr
	}

	// Backward-compatible behavior: with no preset, only create if missing.
	if preset == "" && exists {
		return false, nil
	}

	cfg, err := DefaultForExecutorPreset(preset)
	if err != nil {
		return false, err
	}
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return false, err
	}
	return !exists, nil
}

func PromptDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "forgeworld"), nil
}

func PromptPaths() (map[string]string, error) {
	dir, err := PromptDir()
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"alpha":      filepath.Join(dir, "alpha.md"),
		"error":      filepath.Join(dir, "error.md"),
		"review":     filepath.Join(dir, "review.md"), // kept for backward compat
		"judge":      filepath.Join(dir, "judge.md"),
		"merge":      filepath.Join(dir, "merge.md"),
		"done":       filepath.Join(dir, "done.md"),
		"plan":       filepath.Join(dir, "plan.md"),
		"crit-error": filepath.Join(dir, "crit-error.md"),
	}, nil
}

func ValidatePromptFiles() error {
	paths, err := PromptPaths()
	if err != nil {
		return err
	}
	missing := []string{}
	for _, k := range []string{"alpha", "error", "judge", "merge", "done"} {
		p := paths[k]
		if _, err := os.Stat(p); err != nil {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("faltan prompts globales obligatorios en ~/.config/forgeworld:\n- %s\ncopia plantillas desde templates/prompts/", strings.Join(missing, "\n- "))
	}
	return nil
}

func ReadPrompt(kind string) (string, error) {
	paths, err := PromptPaths()
	if err != nil {
		return "", err
	}
	p, ok := paths[kind]
	if !ok {
		return "", fmt.Errorf("prompt desconocido: %s", kind)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ReadRolePrompt loads a role prompt with fallback:
// loop/roles/<role>.md → ~/.config/forgeworld/<role>.md → ErrRoleNotFound
func ReadRolePrompt(root, role string) (content, sourcePath string, err error) {
	localPath := filepath.Join(root, "loop", "roles", role+".md")
	if b, err := os.ReadFile(localPath); err == nil {
		return string(b), localPath, nil
	}
	dir, err := PromptDir()
	if err != nil {
		return "", "", err
	}
	globalPath := filepath.Join(dir, role+".md")
	b, err := os.ReadFile(globalPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", ErrRoleNotFound
		}
		return "", "", err
	}
	return string(b), globalPath, nil
}

// ListAvailableRoles scans loop/roles/*.md and ~/.config/forgeworld/*.md.
// loop/roles/ takes precedence. alpha and review are excluded.
func ListAvailableRoles(root string) []string {
	seen := make(map[string]bool)
	result := []string{}

	// Project-local roles first
	localDir := filepath.Join(root, "loop", "roles")
	if entries, err := os.ReadDir(localDir); err == nil {
		localNames := []string{}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".md")
			if name == "alpha" || name == "review" {
				continue
			}
			if !seen[name] {
				seen[name] = true
				localNames = append(localNames, name)
			}
		}
		sort.Strings(localNames)
		result = append(result, localNames...)
	}

	// Global roles
	dir, err := PromptDir()
	if err != nil {
		return result
	}
	if entries, err := os.ReadDir(dir); err == nil {
		globalNames := []string{}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".md")
			if name == "alpha" || name == "review" {
				continue
			}
			if !seen[name] {
				seen[name] = true
				globalNames = append(globalNames, name)
			}
		}
		sort.Strings(globalNames)
		result = append(result, globalNames...)
	}

	return result
}
