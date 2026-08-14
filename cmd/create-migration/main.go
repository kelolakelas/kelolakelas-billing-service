package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/migration"
)

func main() {
	name := flag.String("name", "", "migration name")
	directory := flag.String("path", "migrations", "migration directory")
	flag.Parse()
	message, err := migration.CreateMigration(*directory, *name)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(message)
}
