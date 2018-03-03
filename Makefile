GO     = go
DOCKER = docker
PROTOC = protoc
MAKE   = make

GENERATED_SRC = proto/github.proto
GENERATED     = github.pb.go
SRC           = cmd/gitwatch.go

build: generate gitwatch

generate: $(GENERATED)

$(GENERATED): $(GENERATED_SRC)
	protoc --proto_path=./proto --go_out=plugins=grpc,import_path=.:. $<

docker:
	GOARCH=amd64 GOOS="linux" make build $(SRC)
	$(DOCKER) build . -t gitwatch
	rm gitwatch

gitwatch: $(SRC)
	$(GO) build -o $@ $<

clean:
	rm gitwatch