package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"fithub.local/fithub/internal/cli"
	"fithub.local/fithub/internal/mcp"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "mcp" {
		if len(args) > 1 {
			fmt.Fprintln(os.Stderr, "usage: fh mcp (configure FITHUB_URL/FITHUB_TOKEN in the process environment)")
			os.Exit(2)
		}
		if err := mcp.Serve(ctx, os.Stdin, os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(args) > 0 && args[0] == "--version" {
		fmt.Println("fh 0.1.0")
		return
	}
	code := cli.Run(ctx, args, os.Stdin, os.Stdout, os.Stderr)
	if code != 0 {
		os.Exit(code)
	}
}
