# Every target builds static: no cgo, no libc surprises on phones or tiny VPS.
export CGO_ENABLED := 0

BIN := multi-codex-proxy

.PHONY: build run serve test vet tidy clean

build:
	go build -trimpath -o $(BIN) .

run:
	go run .

serve:
	go run . --serve

test:
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -f $(BIN)
