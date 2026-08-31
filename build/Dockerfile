FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w" -o /opentams ./cmd/opentams

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /opentams /opentams

# The :nonroot tag already defaults to uid 65532, but state it explicitly so
# the image cannot be silently re-rooted by a base-image change and so image
# scanners can see the intent.
USER nonroot:nonroot

EXPOSE 8080
ENTRYPOINT ["/opentams"]
CMD ["serve"]
