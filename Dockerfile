# syntax=docker/dockerfile:1
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mybus .

# distroless/static ships CA certificates, needed for 511.org and for
# verifying Alexa's request-signing certificate.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mybus /mybus
ENV PORT=8080
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/mybus"]
