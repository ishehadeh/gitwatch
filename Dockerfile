FROM golang:1.10

WORKDIR /go/src/gitwatch
ADD gitwatch /go/src/gitwatch

CMD ["/go/src/gitwatch/gitwatch", "$HTTP_ADDR", "$GRPC_ADDR"]