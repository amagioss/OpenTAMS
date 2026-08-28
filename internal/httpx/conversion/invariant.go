package conversion

// checkGeneratedInvariant wraps an oapi-codegen From* call whose error
// path is unreachable in correct code (the generated wire structs cannot
// fail json.Marshal). Panic documents the invariant — do NOT substitute
// a silent if-err branch. Mirrors regexp.MustCompile.
func checkGeneratedInvariant(err error) {
	if err != nil {
		panic("conversion: oapi-codegen From* returned error on a generated struct: " + err.Error())
	}
}

// CheckGeneratedInvariantForTest exposes checkGeneratedInvariant to the
// external test package so SCN-CONV-15 can assert the panic prefix
// without depending on internal-test access.
func CheckGeneratedInvariantForTest(err error) { checkGeneratedInvariant(err) }
