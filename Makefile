.PHONY: build cloc dev

build:
	mkdir -p build
	GOOS=linux GOARCH=amd64 go build -o build ./cmd/conway
	GOOS=linux GOARCH=amd64 go build -o build ./cmd/glider

cloc:
	cloc --exclude-dir=vendor --exclude-dir=assets --exclude-ext _templ.go --exclude-ext _test.go .

dev:
	go generate ./modules/...
	cd .dev && go run ../cmd/conway
