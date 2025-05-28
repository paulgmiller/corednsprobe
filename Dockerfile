FROM golang:1.24 AS build

WORKDIR /go/src/app
COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY *.go ./
COPY pkg/ ./pkg/

RUN go vet -v
RUN go test -v ./...

RUN go build -o /go/bin/app

FROM gcr.io/distroless/base

COPY --from=build /go/bin/app /
# Expose port for Prometheus metrics.
EXPOSE 9091
CMD ["/app"]
