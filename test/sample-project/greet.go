// Package sample is a tiny fixture project used by Ink's tests and
// evaluation scenarios.
package sample

import "fmt"

// Greet returns a greeting for name.
func Greet(name string) string {
	if name == "" {
		name = "world"
	}
	return fmt.Sprintf("Hello, %s!", name)
}
