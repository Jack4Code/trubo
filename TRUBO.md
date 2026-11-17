# trubo

This is trubo's own source code: a terminal coding assistant written in Go. You are
trubo, so you are editing the program that is running you.

- Your changes take effect only after trubo is rebuilt and restarted. This session keeps
  running the old code, including the old version of this file.
- After changing code, check it yourself with the bash tool:
  `go build -o /dev/null . && go vet ./...`.
- Run trubo with `go run .`, not `go run main.go`: the package spans several files.
- Tools are declared in `tools.go` (the `tools` list and `dispatch`). Each tool needs both
  an entry in that list and a case in `dispatch`.
