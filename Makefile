.PHONY: build build-preprocess build-server build-visualize test bench vet clean

build: build-preprocess build-server build-visualize

build-preprocess:
	go build -o bin/preprocess ./cmd/preprocess

build-server:
	go build -o bin/server ./cmd/server

build-visualize:
	go build -o bin/visualize ./cmd/visualize

test:
	go test ./... -timeout 60s

test-verbose:
	go test -v ./... -timeout 60s

bench:
	go test -bench=. -benchmem -count=3 ./pkg/geo/ ./pkg/routing/ ./pkg/ch/

vet:
	go vet ./...

clean:
	rm -rf bin/
