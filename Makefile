.PHONY: build build-preprocess build-server test bench clean

build: build-preprocess build-server

build-preprocess:
	go build -o bin/preprocess ./cmd/preprocess

build-server:
	go build -o bin/server ./cmd/server

test:
	go test -v ./...

bench:
	go test -bench=. -benchmem -count=3 ./pkg/routing/

clean:
	rm -rf bin/
