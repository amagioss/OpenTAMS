---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Keep the server out of the media path

## Context and Problem Statement

TAMS clients write and read media segments. A segment is a media file, and a busy Flow
produces them continuously. The API server has to tell a client where the bytes live. It
can also carry the bytes itself.

Does media traffic pass through OpenTAMS?

## Considered Options

* Proxy the bytes: clients `PUT` and `GET` media through OpenTAMS, which streams to and
  from the object store
* Hand out presigned URLs: clients upload and download directly against the object store
* Offer both, and let the operator choose

## Decision Outcome

Chosen option: "Hand out presigned URLs", because it keeps the server's work proportional
to the number of API calls instead of the volume of media.

`objectstore.Store.GenerateUploadURL` and `GenerateDownloadURL` sign S3 requests with a
bounded expiry, set by `OBJECT_STORE_PRESIGN_EXPIRY`. The server never opens a media
body. No non-test code in `internal/` or `cmd/` copies an object stream.

This splits OpenTAMS into a control plane and a data plane. OpenTAMS is the control
plane. The object store is the data plane, and the client talks to it directly.

### Consequences

* Good, because a server instance sized for metadata traffic serves media traffic of any
  volume. Scaling reads means scaling the object store, not OpenTAMS.
* Good, because media bytes never enter the server's memory, which removes a whole class
  of buffering and back-pressure problems.
* Good, because it makes bring-your-own-storage coherent. For BYOS the client already
  owns the bytes, and OpenTAMS only records where they are.
* Bad, because clients must reach the object store's endpoint themselves. In a network
  where only the API is exposed, presigned URLs do not resolve, and the operator has to
  publish the object-store endpoint too.
* Bad, because the expiry is a real constraint. A client that holds a URL longer than
  `OBJECT_STORE_PRESIGN_EXPIRY` gets a signature error, and must ask for a fresh URL.
* Bad, because OpenTAMS cannot enforce per-object authorization at read time. Anyone
  holding a valid presigned URL can use it until it expires.

## More Information

* The signing code: [`internal/objectstore/objectstore.go`](../../internal/objectstore/objectstore.go).
* How this shapes request flow: [`../architecture/data-flows.md`](../architecture/data-flows.md).
* The `get_urls` projection that carries these URLs to the client: [ADR-0026](0026-get-urls-projected-in-the-handler.md).
