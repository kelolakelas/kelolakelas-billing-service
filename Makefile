.PHONY: run test migrate-up migrate-down seed create-migration create-seeder

run:
	go run ./cmd/server

test:
	go test ./...

migrate-up:
	go run ./cmd/migrate -direction up

migrate-down:
	go run ./cmd/migrate -direction down -steps 1

seed:
	go run ./cmd/seed -dir seeders

create-migration:
	test -n "$(name)" || (echo "name is required, for example: make create-migration name=add_indexes" >&2; exit 1)
	go run ./cmd/create-migration -name "$(name)"

create-seeder:
	test -n "$(name)" || (echo "name is required, for example: make create-seeder name=demo_accounts" >&2; exit 1)
	go run ./cmd/create-seeder -name "$(name)"
