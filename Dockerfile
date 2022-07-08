FROM golang:alpine AS builder

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download
COPY main.go main.go
RUN go build -o main main.go

FROM alpine

COPY --from=builder /workspace/main /tmp/main

ENTRYPOINT ["/tmp/main"]