package command

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// discoveredComponentPackage 保存一个分层组件包，所有导入和注册名均来自受检路径。
type discoveredComponentPackage struct {
	layer        string
	directory    string
	namespace    string
	alias        string
	types        []string
	constructors map[string]bool
}

func discoverNestedComponents(semantic *applicationSemanticContext, applicationDirectory string) ([]discoveredComponentPackage, error) {
	root, err := os.OpenRoot(semantic.basePath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var result []discoveredComponentPackage
	for _, layer := range []string{"controller", "model", "validate"} {
		layerDirectory := filepath.Join(applicationDirectory, layer)
		directories, err := nestedComponentDirectories(root, layerDirectory)
		if err != nil {
			return nil, err
		}
		for _, directory := range directories {
			types, _, err := discoverApplicationStructTypesWithContext(semantic, directory, "")
			if err != nil {
				return nil, err
			}
			if layer == "controller" {
				types = excludeApplicationType(types, "BaseController")
			}
			if len(types) == 0 {
				continue
			}
			relative, err := filepath.Rel(layerDirectory, directory)
			if err != nil {
				return nil, err
			}
			component := discoveredComponentPackage{
				layer: layer, directory: directory, namespace: strings.ReplaceAll(filepath.ToSlash(relative), "/", "."),
				alias: fmt.Sprintf("applicationNested%d", len(result)), types: types,
			}
			if layer == "validate" {
				component.constructors, err = discoverApplicationConstructorsWithContext(semantic, directory, "", types)
				if err != nil {
					return nil, err
				}
			}
			result = append(result, component)
		}
	}
	return result, nil
}

func nestedComponentDirectories(root *os.Root, base string) ([]string, error) {
	var result []string
	var walk func(string) error
	walk = func(directory string) error {
		handle, err := root.Open(directory)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		entries, readErr := handle.ReadDir(-1)
		if err := errors.Join(readErr, handle.Close()); err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("组件路径不能是符号链接: %s", filepath.Join(directory, entry.Name()))
			}
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || entry.Name() == "testdata" {
				continue
			}
			if err := validateNativeApplicationPackageName(entry.Name()); err != nil {
				return err
			}
			next := filepath.Join(directory, entry.Name())
			result = append(result, next)
			if err := walk(next); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(base); err != nil {
		return nil, err
	}
	sort.Strings(result)
	return result, nil
}

func (application discoveredNativeApplication) hasNestedLayer(layer string) bool {
	for _, component := range application.nestedComponents {
		if component.layer == layer {
			return true
		}
	}
	return false
}

func writeNestedComponentRegistrations(output *strings.Builder, components []discoveredComponentPackage, layer string) {
	for _, component := range components {
		if component.layer != layer {
			continue
		}
		for _, name := range component.types {
			value := fmt.Sprintf("&%s.%s{}", component.alias, name)
			if layer == "validate" {
				if component.constructors[name] {
					value = fmt.Sprintf("%s.New%s()", component.alias, name)
				}
				value = "func() interface{} { return " + value + " }"
			}
			output.WriteString(fmt.Sprintf("\t\t\t%q: %s,\n", component.namespace+"."+name, value))
		}
	}
}

func nestedApplicationDirectoryHasSource(root *os.Root, base string) (bool, error) {
	directories, err := nestedComponentDirectories(root, base)
	if err != nil {
		return false, err
	}
	for _, directory := range directories {
		handle, err := root.Open(directory)
		if err != nil {
			return false, err
		}
		entries, readErr := handle.ReadDir(-1)
		if err := errors.Join(readErr, handle.Close()); err != nil {
			return false, err
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") && !strings.HasSuffix(entry.Name(), "_test.go") {
				return true, nil
			}
		}
	}
	return false, nil
}
