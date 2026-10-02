package sessionctx

// Boilerplate is the always-present first layer for a session of f.Kind.
func Boilerplate(f SessionFacts) Layer { return Layer{} }
