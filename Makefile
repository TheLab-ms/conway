.PHONY: build

build:
	mkdir -p build
	GOOS=linux GOARCH=amd64 go build -o build ./cmd/conway
	GOOS=linux GOARCH=amd64 go build -o build ./cmd/glider
