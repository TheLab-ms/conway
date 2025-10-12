.PHONY: build cloc dev

build:
	GOOS=linux GOARCH=amd64 go build

cloc:
	cloc --exclude-dir=vendor --exclude-dir=assets --exclude-ext _templ.go --exclude-ext _test.go .

dev:
	go generate ./modules/...
	cd .dev && go run ../
