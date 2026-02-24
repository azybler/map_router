.PHONY: build build-preprocess build-server test bench vet clean

build: build-preprocess build-server

build-preprocess:
	go build -o bin/preprocess ./cmd/preprocess

build-server:
	go build -o bin/server ./cmd/server

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
