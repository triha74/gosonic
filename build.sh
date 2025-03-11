go clean 
go mod tidy
go mod vendor
go test ./...
go build
go install