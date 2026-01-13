.PHONY: all build clean windows linux darwin

VERSION := $(shell grep 'Version:' main.go | head -1 | cut -d'"' -f2)
OUTPUT_DIR := bin

all: clean build

build: windows linux darwin
	@echo "Build complete for all platforms"

windows:
	@echo "Building for Windows..."
	GOOS=windows GOARCH=amd64 go build -o $(OUTPUT_DIR)/x64_windows_chaturbate-dvr.exe
	GOOS=windows GOARCH=arm64 go build -o $(OUTPUT_DIR)/arm64_windows_chaturbate-dvr.exe

linux:
	@echo "Building for Linux..."
	GOOS=linux GOARCH=amd64 go build -o $(OUTPUT_DIR)/x64_linux_chaturbate-dvr
	GOOS=linux GOARCH=arm64 go build -o $(OUTPUT_DIR)/arm64_linux_chaturbate-dvr

darwin:
	@echo "Building for macOS..."
	GOOS=darwin GOARCH=amd64 go build -o $(OUTPUT_DIR)/x64_macos_chaturbate-dvr
	GOOS=darwin GOARCH=arm64 go build -o $(OUTPUT_DIR)/arm64_macos_chaturbate-dvr

clean:
	@echo "Cleaning..."
	rm -rf $(OUTPUT_DIR)
	mkdir -p $(OUTPUT_DIR)

run:
	go run .

docker:
	docker build -t chaturbate-dvr .
