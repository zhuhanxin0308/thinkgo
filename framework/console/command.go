package console

import "thinkgo/framework"

// ICommand interface
type ICommand interface {
	Configure()
	Execute(input *Input, output *Output)
	GetSignature() string
	GetDescription() string
}

// Command base struct
type Command struct {
	Signature   string
	Description string
	App         *framework.App
	Input       *Input
	Output      *Output
}

// Configure configures the command
func (c *Command) Configure() {}

// Execute executes the command
func (c *Command) Execute(input *Input, output *Output) {}

// GetSignature returns the signature
func (c *Command) GetSignature() string {
	return c.Signature
}

// GetDescription returns the description
func (c *Command) GetDescription() string {
	return c.Description
}

// SetApp sets the app instance
func (c *Command) SetApp(app *framework.App) {
	c.App = app
}
