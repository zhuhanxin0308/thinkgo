package command

import (
	"encoding/json"
	"thinkgo/framework/console"
)

// ConfigDump command
type ConfigDump struct {
	console.Command
}

func (c *ConfigDump) Configure() {
	c.Signature = "config:dump"
	c.Description = "Dump configuration values"
}

func (c *ConfigDump) Execute(input *console.Input, output *console.Output) {
	name := input.GetArgument(0)
	
	var data interface{}
	if name != "" {
		data = c.App.Config.Get(name)
	} else {
		data = c.App.Config.Get("")
	}
	
	jsonBytes, err := json.MarshalIndent(data, "", "    ")
	if err != nil {
		output.Error("Failed to marshal config: " + err.Error())
		return
	}
	
	output.Writeln(string(jsonBytes))
}
