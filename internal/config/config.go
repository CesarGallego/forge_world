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

// LocalPromptDir returns the project-local prompt directory (loop/prompts/).
func LocalPromptDir(root string) string {
	return filepath.Join(root, "loop", "prompts")
}

func PromptDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "forgeworld"), nil
}

func ValidatePromptFiles(root string) error {
	missing := []string{}
	for _, k := range []string{"alpha", "error", "judge", "done"} {
		localPath := filepath.Join(root, "loop", "prompts", k+".md")
		if _, err := os.Stat(localPath); err == nil {
			continue
		}
		// Backward compat: check ~/.config/forgeworld/
		dir, err := PromptDir()
		if err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(dir, k+".md")); err != nil {
			missing = append(missing, k+".md")
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("faltan prompts obligatorios. Ejecuta `forgeworld init` para crearlos en loop/prompts/:\n- %s", strings.Join(missing, "\n- "))
	}
	return nil
}

// ReadPrompt loads a prompt by kind.
// Priority: loop/prompts/<kind>.md → ~/.config/forgeworld/<kind>.md (backward compat).
func ReadPrompt(root, kind string) (string, error) {
	localPath := filepath.Join(root, "loop", "prompts", kind+".md")
	if b, err := os.ReadFile(localPath); err == nil {
		return string(b), nil
	}
	// Backward compat: fall back to ~/.config/forgeworld/
	dir, err := PromptDir()
	if err != nil {
		return "", err
	}
	knownKinds := map[string]bool{
		"alpha": true, "error": true, "review": true,
		"judge": true, "merge": true, "done": true,
		"plan": true, "crit-error": true,
	}
	if !knownKinds[kind] {
		return "", fmt.Errorf("prompt desconocido: %s", kind)
	}
	b, err := os.ReadFile(filepath.Join(dir, kind+".md"))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ReadRolePrompt loads a role prompt with fallback:
// loop/roles/<role>.md → loop/prompts/<role>.md → ~/.config/forgeworld/<role>.md → ErrRoleNotFound
func ReadRolePrompt(root, role string) (content, sourcePath string, err error) {
	// 1. Project-local override
	localPath := filepath.Join(root, "loop", "roles", role+".md")
	if b, err := os.ReadFile(localPath); err == nil {
		return string(b), localPath, nil
	}
	// 2. Project prompts directory
	localPromptPath := filepath.Join(root, "loop", "prompts", role+".md")
	if b, err := os.ReadFile(localPromptPath); err == nil {
		return string(b), localPromptPath, nil
	}
	// 3. Global ~/.config/forgeworld/ (backward compat)
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

// ListAvailableRoles scans loop/roles/*.md, loop/prompts/*.md, and ~/.config/forgeworld/*.md.
// loop/roles/ takes precedence. alpha and review are excluded.
func ListAvailableRoles(root string) []string {
	seen := make(map[string]bool)
	result := []string{}

	scanDir := func(dir string) []string {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil
		}
		names := []string{}
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
				names = append(names, name)
			}
		}
		sort.Strings(names)
		return names
	}

	// 1. Project-local role overrides
	result = append(result, scanDir(filepath.Join(root, "loop", "roles"))...)
	// 2. Project prompts directory
	result = append(result, scanDir(filepath.Join(root, "loop", "prompts"))...)
	// 3. Global ~/.config/forgeworld/ (backward compat)
	if dir, err := PromptDir(); err == nil {
		result = append(result, scanDir(dir)...)
	}

	return result
}
