.PHONY: build cloc dev seed clean

build:
	go generate ./...
	GOOS=linux GOARCH=amd64 go build

cloc:
	cloc --exclude-dir=vendor --exclude-dir=assets --exclude-ext _templ.go --exclude-ext _test.go .

dev:
	go generate ./modules/...
	mkdir -p .dev
	cd .dev && go run ../

seed:
	sqlite3 .dev/conway.sqlite3 "INSERT INTO members (email, leadership) VALUES ('dev@localhost', TRUE);"

clean:
	rm -rf .dev
