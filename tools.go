package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
)

// tools is what we tell the model it can ask for. The description is the only
// thing the model reads to decide when to use a tool, so it matters.
var tools = []anthropic.ToolUnionParam{
	{OfTool: &anthropic.ToolParam{
		Name: "get_current_time",
		Description: anthropic.String("Get the current date and time. Use this whenever the user " +
			"asks about the current time or date. Optionally takes an IANA timezone name; " +
			"without one, returns the local time of the machine trubo is running on."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"timezone": map[string]any{
					"type":        "string",
					"description": `IANA timezone name, e.g. "America/New_York" or "Asia/Tokyo". Omit for local time.`,
				},
			},
		},
	}},
}

// dispatch runs the tool the model asked for. A tool request is just data:
// nothing happens unless this function decides to do it. Errors are returned
// to the model as tool results, not treated as fatal.
func dispatch(name string, input json.RawMessage) (string, error) {
	switch name {
	case "get_current_time":
		return getCurrentTime(input)
	default:
		return "", fmt.Errorf("unknown tool %q", name)
	}
}

func getCurrentTime(input json.RawMessage) (string, error) {
	var args struct {
		Timezone string `json:"timezone"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return "", fmt.Errorf("invalid input: %w", err)
		}
	}

	loc := time.Local
	if args.Timezone != "" {
		l, err := time.LoadLocation(args.Timezone)
		if err != nil {
			return "", fmt.Errorf("unknown timezone %q", args.Timezone)
		}
		loc = l
	}

	return time.Now().In(loc).Format("Monday, 2006-01-02 15:04:05 MST"), nil
}
