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
	systemPrompt   = "You are trubo, a coding assistant running in a terminal. Be concise."
)

func main() {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		fmt.Fprintln(os.Stderr, "ANTHROPIC_API_KEY is not set")
		os.Exit(1)
	}

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

		history = append(history, anthropic.NewUserMessage(anthropic.NewTextBlock(input)))

		resp, err := ask(client, history)
		if err != nil {
			history = history[:len(history)-1]
			fmt.Fprintln(os.Stderr, "Request failed:", err)
		} else {
			history = append(history, resp.ToParam())
			fmt.Println("trubo:", replyText(resp))
		}
		fmt.Print("> ")
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "Reading input:", err)
		os.Exit(1)
	}
}

// ask sends the full conversation to the model and returns its response.
func ask(client anthropic.Client, history []anthropic.MessageParam) (*anthropic.Message, error) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	return client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     model,
		MaxTokens: maxTokens,
		System:    []anthropic.TextBlockParam{{Text: systemPrompt}},
		Messages:  history,
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
