.PHONY: build build-linux test vet clean

# Compila el binario para la plataforma actual
build:
	go build -o bot .

# Compila para Linux amd64 (sirve para compilar desde Windows y subir al VPS)
build-linux:
	GOOS=linux GOARCH=amd64 go build -o bot .

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -f bot
