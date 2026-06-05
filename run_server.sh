rm bin/map-router-server
go build -o bin/map-router-server cmd/server/main.go
bin/map-router-server --graph graph.time.bin --port 8086
