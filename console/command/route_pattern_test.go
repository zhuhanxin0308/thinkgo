package command

import (
	"bytes"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

func TestRouteListMorePrintsRegisteredPatterns(t *testing.T) {
	application := buildConsoleTestApp(t, t.TempDir())
	router, err := resolveApplicationRoute(application)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := router.Get("/teams/:team/users/:id", "User/show")
	if err != nil {
		t.Fatal(err)
	}
	for name, pattern := range map[string]string{"team": "[a-z]+", "id": "[0-9]+"} {
		if err := registered.WithPattern(name, pattern); err != nil {
			t.Fatal(err)
		}
	}
	output := &bytes.Buffer{}
	command := &RouteList{Command: console.Command{App: application}}
	if err := command.Execute(console.NewInput("--more"), console.NewOutputWithWriters(output, &bytes.Buffer{}, false)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `{"id":"[0-9]+","team":"[a-z]+"}`) {
		t.Fatalf("路由列表丢失实际参数约束或顺序不稳定: %s", output.String())
	}
}
