package handlers

// ptr returns a pointer to v. oapi-codegen v2.7.x models optional response
// headers as pointers; every header this package emitted before that change
// was written unconditionally, so each one is addressed unconditionally here
// to keep the emitted header set identical.
func ptr[T any](v T) *T { return &v }
