# Build
FROM golang:1.22-alpine AS build
WORKDIR /src
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum ./
COPY *.go ./
COPY static ./static
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /cursorlite .

# Run
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata python3
WORKDIR /app
COPY --from=build /cursorlite /app/cursorlite
ENV WORKSPACE_ROOT=/workspace
ENV LISTEN_ADDR=:8080
EXPOSE 8080
ENTRYPOINT ["/app/cursorlite"]
