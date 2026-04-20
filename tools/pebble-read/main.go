// pebble-read prints the value of a single Pebble key to stdout.
// Usage: go run ./tools/pebble-read <key>
// The Pebble DB path is read from PEBBLE_PATH (or .env).
package main

import (
	"fmt"
	"os"

	"github.com/cockroachdb/pebble"
	"github.com/joho/godotenv"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./tools/pebble-read <key>")
		os.Exit(1)
	}
	key := os.Args[1]

	_ = godotenv.Load()

	dbPath := os.Getenv("PEBBLE_PATH")
	if dbPath == "" {
		fmt.Fprintln(os.Stderr, "PEBBLE_PATH not set")
		os.Exit(1)
	}

	db, err := pebble.Open(dbPath, &pebble.Options{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "open pebble: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	val, closer, err := db.Get([]byte(key))
	if err == pebble.ErrNotFound {
		fmt.Printf("key %q not found\n", key)
		os.Exit(0)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "get: %v\n", err)
		os.Exit(1)
	}
	defer closer.Close()

	fmt.Printf("%s\n", val)
}
