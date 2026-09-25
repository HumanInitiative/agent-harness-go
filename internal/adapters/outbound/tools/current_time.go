package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// datetimeArgs is the shape of arguments the model must supply for
// CurrentTimeTool, mirrored by its InputSchema below.
type datetimeArgs struct {
	// Timezone is an IANA timezone name, e.g. "Asia/Jakarta" or "UTC".
	// Empty defaults to UTC.
	Timezone string `json:"timezone"`
}

// CurrentTimeTool answers "what time is it" questions. It is a good first
// tool to wire up precisely because it is side-effect-free and has no
// external dependency: it exercises the full tool-calling path (schema,
// argument decoding, execution, result formatting) without anything else
// that could fail.
type CurrentTimeTool struct{}

// NewCurrentTimeTool constructs a CurrentTimeTool. It takes no dependencies
// because it has none — real tools that call out to other services accept
// their outbound clients here instead of reaching for globals.
func NewCurrentTimeTool() *CurrentTimeTool { return &CurrentTimeTool{} }

func (t *CurrentTimeTool) Name() string { return "get_current_time" }

func (t *CurrentTimeTool) Description() string {
	return "Returns the current date and time. Optionally accepts an IANA timezone name " +
		"(e.g. \"Asia/Jakarta\"); defaults to UTC when omitted."
}

func (t *CurrentTimeTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"timezone": map[string]any{
				"type":        "string",
				"description": "IANA timezone name, e.g. Asia/Jakarta. Defaults to UTC.",
			},
		},
	}
}

func (t *CurrentTimeTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var in datetimeArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return "", fmt.Errorf("get_current_time: invalid arguments: %w", err)
		}
	}

	loc := time.UTC
	if in.Timezone != "" {
		l, err := time.LoadLocation(in.Timezone)
		if err != nil {
			return "", fmt.Errorf("get_current_time: unknown timezone %q: %w", in.Timezone, err)
		}
		loc = l
	}

	return time.Now().In(loc).Format(time.RFC3339), nil
}
