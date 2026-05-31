rm bin/map-router-visualize
go build -o bin/map-router-visualize cmd/visualize/main.go
bin/map-router-visualize --router-url http://localhost:8086 --port 8088
