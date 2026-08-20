FROM quay.io/hummingbird/go:1.26.7-builder@sha256:7f72e09d2bf486469f48a9bd681acd8fd1af9d31fab247c218a84a8ef9ea5d2c AS build

# Version is injected at build time; the container has no usable .git to derive
# it from (see `make container`). Defaults to "dev" for plain `podman build`.
ARG VERSION=dev

RUN dnf install -y make git && dnf clean all

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux make build VERSION="${VERSION}"

FROM quay.io/hummingbird/core-runtime:2.43@sha256:71993808c91eb67af437cbd08eb03e997b80c6ebb8a376693eb113165b837cec

WORKDIR /app

COPY --from=build /app/forgejo-mcp .

ENTRYPOINT ["/app/forgejo-mcp"]
