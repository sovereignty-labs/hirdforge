package tools

// Coalesce returns the first non-zero value from vals,
// or the zero value of T if all values are zero or vals is empty.
func Coalesce[T comparable](vals ...T) T {
	var zero T
	for _, v := range vals {
		if v != zero {
			return v
		}
	}
	return zero
}
