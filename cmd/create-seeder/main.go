package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/migration"
)

func main() {
	name := flag.String("name", "", "seeder name")
	directory := flag.String("dir", "seeders", "seed SQL directory")
	flag.Parse()
	message, err := migration.CreateSeeder(*directory, *name)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(message)
}
