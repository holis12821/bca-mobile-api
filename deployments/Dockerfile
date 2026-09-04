# deployments/Dockerfile
#
# Build from the repository root:
#   docker build -f deployments/Dockerfile -t bca-mobile-api:local .

FROM golang:1.23-alpine AS build

WORKDIR /src

# Cache the module layer separately from the source layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO off so the binary runs on a distroless static base.
# -s -w strips the symbol table and DWARF info.
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags="-s -w" \
        -o /out/server ./cmd/server

# ---------------------------------------------------------------------------

FROM gcr.io/distroless/static-debian12:nonroot

# No shell, no package manager, no root. If the process is ever compromised
# there is nothing in the image to pivot with.
COPY --from=build /out/server /server

# JWT and PIN keys are mounted read-only at runtime, never baked into the image.
VOLUME ["/keys"]

USER nonroot:nonroot
EXPOSE 8080

ENTRYPOINT ["/server"]
