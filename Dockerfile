FROM golang:1.14-alpine as builder

RUN mkdir -p /build

WORKDIR /build

COPY go.mod /build/
COPY go.sum /build/

RUN go mod download

COPY main.go /build/main.go

RUN CGO_ENABLED=0 go build -o deadmanswitch.exe main.go

FROM alpine:3.11

RUN apk --update --no-cache add ca-certificates

COPY --from=builder /build/deadmanswitch.exe /opt/deadmanswitch

EXPOSE 8080

ENTRYPOINT ["/opt/deadmanswitch", "/etc/deadmanswitch.yaml"]
