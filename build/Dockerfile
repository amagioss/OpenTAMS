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

# The image redistributes every statically linked dependency, so it carries
# the licence and attribution notices with it. /licenses is where the OCI
# image spec's licences label points scanners.
COPY --from=builder /src/LICENSE /src/NOTICE /licenses/
COPY --from=builder /src/THIRD-PARTY-NOTICES /licenses/THIRD-PARTY-NOTICES

# The :nonroot tag already defaults to uid 65532, but state it explicitly so
# the image cannot be silently re-rooted by a base-image change and so image
# scanners can see the intent.
USER nonroot:nonroot

EXPOSE 8080
ENTRYPOINT ["/opentams"]
CMD ["serve"]
