TARGET = helm-schema

ifeq ($(OS),Windows_NT)
    # Windows 系统
	TARGET = helm-schema.exe
endif


.PHONY: build
build: 
	@go build -x -o bin/$(TARGET)  cmd/helm-schema/main.go cmd/helm-schema/cli.go cmd/helm-schema/version.go
	@echo "build successfully"