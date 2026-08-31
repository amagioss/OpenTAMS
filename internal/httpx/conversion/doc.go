// Package conversion bridges oapi-codegen wire types (gen/api) and pure
// domain types (internal/domain) for the /flows/{flowId}/segments path.
//
// Pure-shape mappers and request decoders only — no business logic, no
// metastore, no logging, no metrics, no I/O. The package selects neither
// HTTP status nor strict-server response variant; it produces wire-shape
// pieces (api.FlowSegment, api.FlowSegmentBulkFailure, etc.) that the
// handler combines into a 200/201/etc. response.
//
// This package is pure: it must not import the service, metastore, or
// store layers. The `conversion-is-pure` depguard rule in .golangci.yml
// enforces the import shape; SCN-CONV-16 and SCN-HTTP-41 are the runtime
// backstops for the symbol-level half depguard cannot see.
package conversion
