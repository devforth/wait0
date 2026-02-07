# syntax=docker/dockerfile:1

FROM golang:1.22-alpine AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates git

COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/wait0 ./cmd/wait0

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
WORKDIR /
COPY --from=build /out/wait0 /wait0

# Default config path is /wait0.yaml (overridable via WAIT0_CONFIG)
EXPOSE 8082
VOLUME ["/data"]

ENV WAIT0_CONFIG=/wait0.yaml

CMD ["/wait0", "-config", "/wait0.yaml"]
