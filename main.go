package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
)

const (
	model          = "claude-opus-5-5"
	maxTokens      = 16000
	requestTimeout = 5 * time.Minute
	basePrompt     = "You are trubo, a coding assistant running in a terminal. Be concise."
)

// systemPrompt tells the model where it is. It's built at request time
// because workDir isn't known until main runs.
func systemPrompt() string {
	return basePrompt + "\n\nWorking directory: " + workDir +
		"\nThis directory contains trubo's own source code (Go). Use list_files and read_file " +
		"to look at the code instead of guessing."
}

func main() {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		fmt.Fprintln(os.Stderr, "ANTHROPIC_API_KEY is not set")
		os.Exit(1)
	}

	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Getting working directory:", err)
		os.Exit(1)
	}
	workDir = wd

	client := anthropic.NewClient()
	scanner := bufio.NewScanner(os.Stdin)

	// The whole conversation. Since the model has no memory of its own, every
	// request sends this entire list, and we append to it after each turn.
	var history []anthropic.MessageParam

	fmt.Print("> ")
	for scanner.Scan() {
		input := strings.TrimSpace(scanner.Text())

		switch input {
		case "/exit":
			return
		case "/clear":
			history = nil
			fmt.Println("(conversation cleared)")
			fmt.Print("> ")
			continue
		case "":
			fmt.Print("> ")
			continue
		}

		turnStart := len(history)
		history = append(history, anthropic.NewUserMessage(anthropic.NewTextBlock(input)))

		var err error
		history, err = runTurn(client, history)
		if err != nil {
			history = history[:turnStart]
			fmt.Fprintln(os.Stderr, "Request failed:", err)
		}
		fmt.Print("> ")
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "Reading input:", err)
		os.Exit(1)
	}
}

func runTurn(client anthropic.Client, history []anthropic.MessageParam) ([]anthropic.MessageParam, error) {
	for {
		resp, err := ask(client, history)
		if err != nil {
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

		if resp.StopReason != anthropic.StopReasonToolUse {
			return history, nil
		}

		var results []anthropic.ContentBlockParamUnion
		for _, block := range resp.Content {
			use, ok := block.AsAny().(anthropic.ToolUseBlock)
			if !ok {
				continue
			}

			fmt.Printf("[tool] %s %s\n", use.Name, use.Input)
			output, err := dispatch(use.Name, use.Input)
			if err != nil {
				fmt.Printf("[tool] error: %v\n", err)
				results = append(results, anthropic.NewToolResultBlock(use.ID, err.Error(), true))
				continue
			}
			results = append(results, anthropic.NewToolResultBlock(use.ID, output, false))
		}
		history = append(history, anthropic.NewUserMessage(results...))
	}
}

// ask sends the full conversation to the model and returns its response.
func ask(client anthropic.Client, history []anthropic.MessageParam) (*anthropic.Message, error) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	return client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     model,
		MaxTokens: maxTokens,
		System:    []anthropic.TextBlockParam{{Text: systemPrompt()}},
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
