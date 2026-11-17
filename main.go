package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
)

const (
	model          = "claude-opus-5-5"
	maxTokens      = 16000
	requestTimeout = 5 * time.Minute
	maxToolRounds  = 25 // model requests per turn before trubo stops the turn
	basePrompt     = "You are trubo, a coding assistant running in a terminal. Be concise."

	// toolsPrompt is true in any project, so it's built in. Anything about a
	// specific project belongs in that project's TRUBO.md.
	toolsPrompt = "Use list_files and read_file to look at the code instead of guessing. " +
		"Use edit_file to change existing files and write_file only for new files or full " +
		"rewrites. Use bash to run commands, such as building and testing your changes; check " +
		"your own changes that way instead of asking the user to. The user approves every " +
		"change and every command."

	projectFile         = "TRUBO.md" // project instructions, read from workDir at startup
	maxProjectFileBytes = 50 * 1024
)

// systemPrompt is built once at startup, after workDir and the project file
// are known.
var systemPrompt string

// buildSystemPrompt combines the built-in instructions with the project's
// TRUBO.md, if it has one.
func buildSystemPrompt(projectNotes string) string {
	prompt := basePrompt + "\n\nWorking directory: " + workDir + "\n" + toolsPrompt
	if projectNotes != "" {
		prompt += "\n\nInstructions from this project's " + projectFile + ":\n\n<project_instructions>\n" +
			projectNotes + "\n</project_instructions>"
	}
	return prompt
}

// loadProjectFile returns the contents of TRUBO.md in workDir, or "" if the
// project doesn't have one.
func loadProjectFile() (string, error) {
	data, err := os.ReadFile(filepath.Join(workDir, projectFile))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if len(data) > maxProjectFileBytes {
		return "", fmt.Errorf("it is %d bytes; the limit is %d", len(data), maxProjectFileBytes)
	}
	return strings.TrimSpace(string(data)), nil
}

func main() {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		fmt.Fprintln(os.Stderr, "ANTHROPIC_API_KEY is not set")
		os.Exit(1)
	}

	// Resolve symlinks so resolvePath's comparisons see real paths on both sides.
	wd, err := os.Getwd()
	if err == nil {
		wd, err = filepath.EvalSymlinks(wd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Getting working directory:", err)
		os.Exit(1)
	}
	workDir = wd

	notes, err := loadProjectFile()
	switch {
	case err != nil:
		fmt.Fprintf(os.Stderr, "Skipping %s: %v\n", projectFile, err)
	case notes != "":
		fmt.Printf("(loaded %s)\n", projectFile)
	}
	systemPrompt = buildSystemPrompt(notes)

	client := anthropic.NewClient()

	// Ctrl-C cancels the current turn (or prints a hint at the prompt)
	// instead of killing trubo.
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)

	// One goroutine owns stdin and sends each line on lines, closing it at
	// EOF. A blocked Scan can't be interrupted, so the prompt loop and confirm
	// select on this channel instead; a second reader would also miss input
	// the scanner already buffered. readErr is set before lines is closed.
	lines := make(chan string)
	var readErr error
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		readErr = scanner.Err()
		close(lines)
	}()

	confirm := func(ctx context.Context, question string) bool {
		fmt.Print(question + " [y/N] ")
		select {
		case <-ctx.Done():
			fmt.Println()
			return false
		case line, ok := <-lines:
			if !ok {
				return false
			}
			answer := strings.ToLower(strings.TrimSpace(line))
			return answer == "y" || answer == "yes"
		}
	}

	// The whole conversation. Since the model has no memory of its own, every
	// request sends this entire list, and we append to it after each turn.
	var history []anthropic.MessageParam

	for {
		fmt.Print("> ")
		var input string
		select {
		case <-interrupts:
			fmt.Println("\n(type /exit or press Ctrl-D to quit)")
			continue
		case line, ok := <-lines:
			if !ok {
				if readErr != nil {
					fmt.Fprintln(os.Stderr, "Reading input:", readErr)
					os.Exit(1)
				}
				return
			}
			input = strings.TrimSpace(line)
		}

		switch input {
		case "/exit":
			return
		case "/clear":
			history = nil
			fmt.Println("(conversation cleared)")
			continue
		case "":
			continue
		}

		turnStart := len(history)
		history = append(history, anthropic.NewUserMessage(anthropic.NewTextBlock(input)))

		// While the turn runs, Ctrl-C cancels its context.
		ctx, cancel := context.WithCancel(context.Background())
		turnDone := make(chan struct{})
		go func() {
			select {
			case <-interrupts:
				cancel()
			case <-turnDone:
			}
		}()

		var err error
		history, err = runTurn(ctx, client, history, confirm)
		close(turnDone)
		cancel()

		if err != nil {
			interrupted := errors.Is(err, context.Canceled)
			if interrupted {
				fmt.Println("(interrupted)")
			} else {
				fmt.Fprintln(os.Stderr, "Turn stopped:", err)
			}
			if len(history) == turnStart+1 {
				// Nothing ran, so forget the message and let the user retry.
				history = history[:turnStart]
			} else {
				// Tools already ran and their effects (like edited files) are
				// real, so keep the record of them. History ends with tool
				// results here; close the turn so user and assistant messages
				// still alternate.
				note := fmt.Sprintf("(This turn was interrupted: %v. The tool calls above were carried out.)", err)
				if interrupted {
					note = "(The user interrupted this turn. The tool results above show which tool calls were carried out.)"
				}
				history = append(history, anthropic.NewAssistantMessage(anthropic.NewTextBlock(note)))
			}
		}
	}
}

// runTurn sends the conversation, runs any tools the model asks for, and
// repeats until the model replies without asking for a tool. If ctx is
// cancelled, it returns ctx.Err() with history still valid to send again.
func runTurn(ctx context.Context, client anthropic.Client, history []anthropic.MessageParam, confirm confirmFunc) ([]anthropic.MessageParam, error) {
	for round := 1; ; round++ {
		if round > maxToolRounds {
			return history, fmt.Errorf("stopped after %d rounds of tool calls", maxToolRounds)
		}

		resp, err := ask(ctx, client, history)
		if err != nil {
			if ctx.Err() != nil {
				return history, ctx.Err()
			}
			return history, err
		}
		if resp.StopReason == anthropic.StopReasonRefusal {
			return history, fmt.Errorf("model declined to respond")
		}

		// Store the full response (thinking and tool_use blocks included).
		history = append(history, resp.ToParam())

		if text := replyText(resp); text != "" {
			fmt.Println("trubo:", text)
		}

		// Run every tool call in the response. This checks the content rather
		// than StopReason == tool_use: a response cut off by max_tokens can
		// still end in a tool_use block, and the API rejects the next request
		// unless every tool_use gets a tool_result.
		var results []anthropic.ContentBlockParamUnion
		for _, block := range resp.Content {
			use, ok := block.AsAny().(anthropic.ToolUseBlock)
			if !ok {
				continue
			}

			// After an interrupt, still answer every tool_use, without running it.
			if ctx.Err() != nil {
				results = append(results, anthropic.NewToolResultBlock(use.ID,
					"This tool call was not run because the user interrupted the turn.", true))
				continue
			}

			fmt.Printf("[tool] %s %s\n", use.Name, truncate(string(use.Input), 200))
			output, err := dispatch(ctx, use.Name, use.Input, confirm)
			if err != nil {
				fmt.Printf("[tool] error: %v\n", err)
				results = append(results, anthropic.NewToolResultBlock(use.ID, err.Error(), true))
				continue
			}
			results = append(results, anthropic.NewToolResultBlock(use.ID, output, false))
		}
		if len(results) == 0 {
			return history, nil
		}
		history = append(history, anthropic.NewUserMessage(results...))
		if ctx.Err() != nil {
			return history, ctx.Err()
		}
	}
}

// truncate shortens s for display, so a write_file call doesn't print the
// whole file as its input.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ask sends the full conversation to the model and returns its response.
func ask(ctx context.Context, client anthropic.Client, history []anthropic.MessageParam) (*anthropic.Message, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	return client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     model,
		MaxTokens: maxTokens,
		System:    []anthropic.TextBlockParam{{Text: systemPrompt}},
		Messages:  history,
		Tools:     tools,
	})
}

func replyText(resp *anthropic.Message) string {
	var reply strings.Builder
	for _, block := range resp.Content {
		if text, ok := block.AsAny().(anthropic.TextBlock); ok {
			reply.WriteString(text.Text)
		}
	}
	return reply.String()
}
