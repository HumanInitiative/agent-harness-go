package outbound

import (
	"context"
	"encoding/json"
)

// ToolHandler is implemented by every tool the harness can offer to the
// model. It deliberately says nothing about Genkit: Name/Description/
// InputSchema give the model everything it needs to decide whether and how
// to call the tool, and Execute runs it once the model does.
//
// Concrete tools live under internal/adapters/outbound/tools — each one is
// an outbound adapter, since "call a tool" is, from the application's point
// of view, exactly the same kind of dependency as "call an HTTP API" or
// "read a database": something the domain asks a port for without knowing
// how it is actually carried out.
type ToolHandler interface {
	// Name is the identifier the model uses to request this tool. It must
	// be stable — changing it is a breaking change for any prompt or eval
	// fixture that references it by name.
	Name() string

	// Description tells the model when to use this tool. It is prompt, not
	// documentation for humans, and is worth writing as carefully as one:
	// a vague description produces a model that calls the tool at the
	// wrong times or not at all.
	Description() string

	// InputSchema is a JSON Schema object describing the tool's expected
	// arguments, e.g.:
	//
	//	{
	//	  "type": "object",
	//	  "properties": {"city": {"type": "string"}},
	//	  "required": ["city"]
	//	}
	InputSchema() map[string]any

	// Execute runs the tool with the arguments the model supplied, already
	// encoded as a JSON object matching InputSchema. Execute is
	// responsible for its own validation of args — InputSchema guides the
	// model, it does not guarantee the model always complies.
	//
	// The returned string is fed back to the model verbatim as the tool's
	// result, so it should be compact and model-readable (a short summary
	// or small JSON blob), not a large raw payload.
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}
