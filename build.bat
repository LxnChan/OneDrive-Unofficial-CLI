@echo off

echo Build for Windows amd64
set GOOS=windows
set GOARCH=amd64
set CGO_ENABLED=
go build -o release/onedrivecli-windows-amd64.exe ./cmd/onedrivecli

echo Build for Linux amd64
set GOOS=linux
set GOARCH=amd64
set CGO_ENABLED=0
go build -o release/onedrivecli-linux-amd64 ./cmd/onedrivecli

echo Build for Linux arm64
set GOOS=linux
set GOARCH=arm64
set CGO_ENABLED=0
go build -o release/onedrivecli-linux-arm64 ./cmd/onedrivecli

set GOOS=
set GOARCH=
set CGO_ENABLED=

echo Done.