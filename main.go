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
)

func main() {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		fmt.Fprintln(os.Stderr, "ANTHROPIC_API_KEY is not set")
		os.Exit(1)
	}

	client := anthropic.NewClient()
	scanner := bufio.NewScanner(os.Stdin)

	fmt.Print("> ")
	for scanner.Scan() {
		input := strings.TrimSpace(scanner.Text())

		if input == "/exit" {
			return
		}
		if input == "" {
			fmt.Print("> ")
			continue
		}

		reply, err := ask(client, input)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Request failed:", err)
		} else {
			fmt.Println("trubo:", reply)
		}
		fmt.Print("> ")
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "Reading input:", err)
		os.Exit(1)
	}
}

func ask(client anthropic.Client, input string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     model,
		MaxTokens: maxTokens,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(input)),
		},
	})
	if err != nil {
		return "", err
	}

	var reply strings.Builder
	for _, block := range resp.Content {
		if text, ok := block.AsAny().(anthropic.TextBlock); ok {
			reply.WriteString(text.Text)
		}
	}
	return reply.String(), nil
}
