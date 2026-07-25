package tools

// Zero returns the zero value of T.
func Zero[T any]() T {
	var zero T
	return zero
}
