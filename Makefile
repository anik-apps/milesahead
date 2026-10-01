.PHONY: build test test-js run clean lint

build:
	go build -o milesahead ./cmd/milesahead

test:
	go test ./... -v

run: build
	./milesahead -config config.yaml

clean:
	rm -f milesahead

lint:
	go vet ./...

# The admin pages' browser-side logic (static/js/admin.js); needs Node 18+.
test-js:
	node --test tests/js/*.test.mjs
