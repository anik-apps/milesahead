.PHONY: build test run clean lint

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
