.PHONY: build test clean install

build:
	go build -o .build/aperture ./cmd/aperture

test:
	go test ./...

install:
	go install ./cmd/aperture

clean:
	rm -rf .build/
