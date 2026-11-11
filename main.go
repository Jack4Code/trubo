package main

import (
	"bufio"
	"fmt"
	"os"
)

func main() {
	scanner := bufio.NewScanner(os.Stdin)

	fmt.Print("You: ")
	for scanner.Scan() {
		input := scanner.Text()

		if input == "/exit" {
			return
		}

		fmt.Println("Echo:", input)
		fmt.Print("You: ")
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "Reading input:", err)
		os.Exit(1)
	}
}
