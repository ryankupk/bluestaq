FROM golang:1.27 AS builder
WORKDIR /src

# Copy module files first so dependency downloads are cached across code changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/notesd ./cmd/notesd

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/notesd /notesd
EXPOSE 8080
ENTRYPOINT ["/notesd"]
