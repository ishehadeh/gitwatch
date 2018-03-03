FROM golang:1.10

WORKDIR /go/src/gitview
ADD gitview /go/src/gitview

CMD ["/go/src/gitview/gitview", "$HTTP_ADDR", "$GRPC_ADDR"]