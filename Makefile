.PHONY: build build-preprocess build-server build-visualize test bench vet clean download-osm

build: build-preprocess build-server build-visualize

build-preprocess:
	go build -o bin/map-router-preprocess ./cmd/preprocess

build-server:
	go build -o bin/map-router-server ./cmd/server

build-visualize:
	go build -o bin/map-router-visualize ./cmd/visualize

test:
	go test ./... -timeout 60s

test-verbose:
	go test -v ./... -timeout 60s

bench:
	go test -bench=. -benchmem -count=3 ./pkg/geo/ ./pkg/routing/ ./pkg/ch/

vet:
	go vet ./...

download-osm:
	curl -L -o malaysia-singapore-brunei-latest.osm.pbf \
		https://download.geofabrik.de/asia/malaysia-singapore-brunei-latest.osm.pbf

clean:
	rm -rf bin/
