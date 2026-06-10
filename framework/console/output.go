package console

import (
	"fmt"
)

// Output handles console output
type Output struct{}

// NewOutput creates a new Output
func NewOutput() *Output {
	return &Output{}
}

// Write writes a message
func (o *Output) Write(msg string) {
	fmt.Print(msg)
}

// Writeln writes a line
func (o *Output) Writeln(msg string) {
	fmt.Println(msg)
}

// Info writes an info message (green)
func (o *Output) Info(msg string) {
	fmt.Printf("\033[32m%s\033[0m\n", msg)
}

// Error writes an error message (red)
func (o *Output) Error(msg string) {
	fmt.Printf("\033[31m%s\033[0m\n", msg)
}

// Warning writes a warning message (yellow)
func (o *Output) Warning(msg string) {
	fmt.Printf("\033[33m%s\033[0m\n", msg)
}

// Success writes a success message (green)
func (o *Output) Success(msg string) {
	fmt.Printf("\033[32m%s\033[0m\n", msg)
}
