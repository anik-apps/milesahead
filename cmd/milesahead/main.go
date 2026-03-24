package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/kumaranik/milesahead/internal/config"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}
	fmt.Fprintf(os.Stdout, "Miles Ahead starting on port %d\n", cfg.Server.Port)
}
