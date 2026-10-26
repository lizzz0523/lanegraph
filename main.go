package main

import (
	"log"
	"os"
)

func main() {
	repo, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	if len(os.Args) > 1 {
		repo = os.Args[1]
	}

	st, err := newState(repo)
	if err != nil {
		log.Fatal(err)
	}

	if err := (&tui{state: st}).run(); err != nil {
		log.Fatal(err)
	}
}
