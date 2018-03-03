GO     = go
DOCKER = docker
PROTOC = protoc
MAKE   = make

GENERATED_SRC = proto/gitview.proto
GENERATED     = gitview_pb/gitview.pb.go
SRC           = cmd/main.go

build: gitview

$(GENERATED): $(GENERATED_SRC)
	protoc --proto_path=./proto --go_out=plugins=grpc,import_path=.:. $<

docker:
	GOARCH=amd64 GOOS="linux" make build $(SRC)
	$(DOCKER) build . -t gitview
	rm gitview

gitview: $(SRC)
	$(GO) build -o $@ $<

clean:
	rm gitview