package tools

// Reverse returns a reversed copy of the input slice.
func Reverse[T any](s []T) []T {
	n := len(s)
	r := make([]T, n)
	for i, v := range s {
		r[n-1-i] = v
	}
	return r
}
