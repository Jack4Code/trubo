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
	{OfTool: &anthropic.ToolParam{
		Name: "read_file",
		Description: anthropic.String("Read a text file in the project. Each line of the output starts " +
			"with its line number and a tab; those numbers are not part of the file. Long files are " +
			"cut off with a note saying so; use offset and limit to read the rest."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "File path, relative to the project root.",
				},
				"offset": map[string]any{
					"type":        "integer",
					"description": "Line number to start reading from (1-based). Defaults to 1.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of lines to return. Defaults to 2000, which is also the maximum.",
				},
			},
			Required: []string{"path"},
		},
	}},
	{OfTool: &anthropic.ToolParam{
		Name: "list_files",
		Description: anthropic.String("List files in the project recursively, as paths relative to " +
			"the project root. Skips .git. Use this to find files before reading them."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Directory to list, relative to the project root. Omit for the whole project.",
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
	case "read_file":
		return readFile(input)
	case "list_files":
		return listFiles(input)
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
