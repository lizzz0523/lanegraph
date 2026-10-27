package main

import (
	"context"
	"log"
	"os"

	"lanegraph/internal/core"
	"lanegraph/internal/tui"
)

func main() {
	repo, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	if len(os.Args) > 1 {
		repo = os.Args[1]
	}

	st, err := core.NewState(context.Background(), repo)
	if err != nil {
		log.Fatal(err)
	}

	if err := tui.New(&st).Run(); err != nil {
		log.Fatal(err)
	}
}
