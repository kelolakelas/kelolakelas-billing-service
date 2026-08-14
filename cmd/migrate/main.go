package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/migration"
)

func main() {
	direction := flag.String("direction", "up", "migration direction: up or down")
	path := flag.String("path", "migrations", "migration directory")
	steps := flag.Int("steps", 1, "number of down migrations")
	flag.Parse()
	if err := migration.Run(*path, *direction, *steps); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("migrations completed")
}
