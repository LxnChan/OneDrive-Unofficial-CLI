set GOOS=
set GOARCH=
set CGO_ENABLED=
go build -o release/onedrivecli-windows-amd64.exe ./cmd/onedrivecli

set GOOS=linux
set GOARCH=amd64
set CGO_ENABLED=0
go build -o release/onedrivecli-linux-amd64 ./cmd/onedrivecli

set GOOS=linux
set GOARCH=arm64
set CGO_ENABLED=0
go build -o release/onedrivecli-linux-arm64 ./cmd/onedrivecli

set GOOS=
set GOARCH=
set CGO_ENABLED=