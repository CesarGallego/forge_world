package forgeworld

import "embed"

// TemplateFS embeds the prompt templates so release binaries can initialize
// prompt files without depending on the repository checkout.
//
//go:embed templates/prompts/*.md
var TemplateFS embed.FS
