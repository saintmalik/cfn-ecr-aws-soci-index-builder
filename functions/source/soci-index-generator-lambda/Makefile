.PHONY: build clean

build:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bootstrap handler.go
	zip lambda-deployment.zip bootstrap
	rm bootstrap

clean:
	rm -f lambda-deployment.zip bootstrap