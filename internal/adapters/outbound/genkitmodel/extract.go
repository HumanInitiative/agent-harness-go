package genkitmodel

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
)

// ExtractJSON asks the model for a JSON answer constrained by schema. It
// satisfies csr.Extractor, so the CSR package can use the harness's model
// without knowing it is Genkit or Gemini.
//
// The instruction and content go in as verbatim parts, never through
// WithSystem/WithPrompt: those may be compiled as templates, and web
// content can contain anything, including template markers.
func (a *Adapter) ExtractJSON(ctx context.Context, instruction, content, schema string) ([]byte, error) {
	var schemaMap map[string]any
	if err := json.Unmarshal([]byte(schema), &schemaMap); err != nil {
		return nil, fmt.Errorf("genkitmodel: extraction schema is not valid JSON: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("genkitmodel: extract: %w", err)
	}

	resp, err := genkit.Generate(ctx, a.g,
		ai.WithModelName(a.model),
		ai.WithSystemParts(ai.NewTextPart(instruction)),
		ai.WithMessages(ai.NewUserTextMessage(content)),
		ai.WithOutputSchema(schemaMap),
	)
	if err != nil {
		return nil, classifyError(ctx, err)
	}
	return []byte(resp.Text()), nil
}
