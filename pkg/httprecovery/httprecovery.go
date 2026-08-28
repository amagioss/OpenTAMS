package httprecovery

import "net/http"

// ResponseWriter is the contract for writing a panic error response.
// recovered is the value passed to panic; stack is the goroutine stack trace.
type ResponseWriter func(w http.ResponseWriter, recovered any, stack []byte)
