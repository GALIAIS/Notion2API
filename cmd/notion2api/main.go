package main

import (
	"log"

	"notion2api/internal/app"
	"notion2api/internal/wreq"
)

func main() {
	log.Printf("notion2api: wreq backend = %s", wreq.Version())
	app.Main()
}
