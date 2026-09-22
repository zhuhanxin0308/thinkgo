package view

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/debug"
)

type recordingViewDriver struct {
	configErr error
	funcErr   error
	renderErr error
	config    map[string]interface{}
	funcs     map[string]interface{}
	data      map[string]interface{}
}

func (d *recordingViewDriver) Config(config map[string]interface{}) error {
	if d.configErr != nil {
		return d.configErr
	}
	d.config = config
	config["driver_mutation"] = true
	return nil
}

func (d *recordingViewDriver) Fetch(_ string, data map[string]interface{}) (string, error) {
	d.data = data
	if d.renderErr != nil {
		return "", d.renderErr
	}
	return "rendered", nil
}

func (d *recordingViewDriver) Display(writer io.Writer, _ string, data map[string]interface{}) error {
	d.data = data
	if d.renderErr != nil {
		return d.renderErr
	}
	_, err := io.WriteString(writer, "rendered")
	return err
}

func (d *recordingViewDriver) Exists(string) (bool, error) {
	return true, d.renderErr
}

func (d *recordingViewDriver) SetFuncMap(funcMap map[string]interface{}) error {
	if d.funcErr != nil {
		return d.funcErr
	}
	d.funcs = funcMap
	return nil
}

// TestViewDriverLifecycleErrors 验证驱动缺失、空驱动和驱动配置失败都通过 error 安全传播。
func TestViewDriverLifecycleErrors(t *testing.T) {
	manager := NewView(nil, nil)
	if _, err := manager.Fetch("index", nil); !errors.Is(err, ErrViewDriverNotConfigured) {
		t.Fatalf("未配置驱动时应返回 ErrViewDriverNotConfigured，实际为 %v", err)
	}
	if err := manager.SetDriver(nil); !errors.Is(err, ErrViewDriverNotConfigured) {
		t.Fatalf("空驱动应返回 ErrViewDriverNotConfigured，实际为 %v", err)
	}

	configErr := errors.New("invalid driver config")
	if err := manager.SetDriver(&recordingViewDriver{configErr: configErr}); !errors.Is(err, configErr) {
		t.Fatalf("驱动配置错误应原样传播，实际为 %v", err)
	}
	if _, err := manager.Fetch("index", nil); !errors.Is(err, ErrViewDriverNotConfigured) {
		t.Fatalf("配置失败的驱动不得被安装，实际为 %v", err)
	}
}

// TestViewMergesDataAndTracksSuccessfulTemplate 验证局部数据优先、配置隔离以及仅成功渲染才记录模板文件。
func TestViewMergesDataAndTracksSuccessfulTemplate(t *testing.T) {
	trace := debug.NewRequestDebug(true)
	config := map[string]interface{}{"view_path": "views"}
	manager := NewView(trace, config)
	driver := &recordingViewDriver{}
	if err := manager.SetDriver(driver); err != nil {
		t.Fatalf("安装测试驱动失败: %v", err)
	}
	if _, mutated := config["driver_mutation"]; mutated {
		t.Fatal("驱动修改配置副本不得污染调用方配置")
	}
	manager.Assign("title", "global")
	manager.Assign("site", "ThinkGo")

	content, err := manager.Fetch("user/index", map[string]interface{}{"title": "local"})
	if err != nil || content != "rendered" {
		t.Fatalf("渲染结果错误: content=%q err=%v", content, err)
	}
	if !reflect.DeepEqual(driver.data, map[string]interface{}{"title": "local", "site": "ThinkGo"}) {
		t.Fatalf("模板数据合并错误: %#v", driver.data)
	}
	files := trace.GetInfo()["files"].([]string)
	if !reflect.DeepEqual(files, []string{"user/index"}) {
		t.Fatalf("成功模板应被记录一次，实际为 %#v", files)
	}

	driver.renderErr = errors.New("render failed")
	if err := manager.Render(&strings.Builder{}, "broken", nil); !errors.Is(err, driver.renderErr) {
		t.Fatalf("渲染错误应传播，实际为 %v", err)
	}
	files = trace.GetInfo()["files"].([]string)
	if !reflect.DeepEqual(files, []string{"user/index"}) {
		t.Fatalf("失败模板不得写入调试文件列表，实际为 %#v", files)
	}
}

// TestViewFunctionAndExistsErrors 验证函数注册与存在性检查不吞掉驱动错误。
func TestViewFunctionAndExistsErrors(t *testing.T) {
	manager := NewView(nil, map[string]interface{}{})
	driver := &recordingViewDriver{}
	if err := manager.SetDriver(driver); err != nil {
		t.Fatalf("安装测试驱动失败: %v", err)
	}
	if err := manager.SetFuncMap(map[string]interface{}{"upper": strings.ToUpper}); err != nil {
		t.Fatalf("注册模板函数失败: %v", err)
	}
	if _, ok := driver.funcs["upper"]; !ok {
		t.Fatal("模板函数没有传递给驱动")
	}

	driver.funcErr = errors.New("invalid function")
	if err := manager.SetFuncMap(map[string]interface{}{"bad": 1}); !errors.Is(err, driver.funcErr) {
		t.Fatalf("函数注册错误应传播，实际为 %v", err)
	}
	driver.renderErr = errors.New("stat failed")
	if _, err := manager.Exists("index"); !errors.Is(err, driver.renderErr) {
		t.Fatalf("存在性检查错误应传播，实际为 %v", err)
	}
}

// TestViewPublicAccessorsAndRenderBoundaries 验证共享数据读取、成功渲染以及空写入器和未配置驱动的错误边界。
func TestViewPublicAccessorsAndRenderBoundaries(t *testing.T) {
	manager := NewView(nil, nil)
	manager.Assign("site", "ThinkGo")
	if value := manager.Get("site"); value != "ThinkGo" {
		t.Fatalf("共享视图数据读取错误，实际为 %#v", value)
	}
	if value := manager.Get("missing"); value != nil {
		t.Fatalf("不存在的共享数据应返回 nil，实际为 %#v", value)
	}
	if err := manager.SetFuncMap(nil); !errors.Is(err, ErrViewDriverNotConfigured) {
		t.Fatalf("未配置驱动时注册函数应失败，实际为 %v", err)
	}
	if _, err := manager.Exists("index"); !errors.Is(err, ErrViewDriverNotConfigured) {
		t.Fatalf("未配置驱动时检查模板应失败，实际为 %v", err)
	}

	driver := &recordingViewDriver{}
	if err := manager.SetDriver(driver); err != nil {
		t.Fatalf("安装视图驱动失败: %v", err)
	}
	if err := manager.Render(nil, "index", nil); !errors.Is(err, ErrInvalidViewWriter) {
		t.Fatalf("空视图写入器应返回 ErrInvalidViewWriter，实际为 %v", err)
	}
	var output strings.Builder
	if err := manager.Render(&output, "index", map[string]interface{}{"page": 1}); err != nil {
		t.Fatalf("渲染视图失败: %v", err)
	}
	if output.String() != "rendered" || driver.data["site"] != "ThinkGo" || driver.data["page"] != 1 {
		t.Fatalf("视图渲染或数据合并错误，输出=%q，数据=%#v", output.String(), driver.data)
	}
}

// TestViewDebugIsolationAndLegacyCompatibility 验证显式请求 collector 相互隔离，旧入口仍使用构造时 collector。
func TestViewDebugIsolationAndLegacyCompatibility(t *testing.T) {
	legacyCollector := debug.NewRequestDebug(true)
	collectorA := debug.NewRequestDebug(true)
	collectorB := debug.NewRequestDebug(true)
	manager := NewView(legacyCollector, nil)
	driver := &recordingViewDriver{}
	if err := manager.SetDriver(driver); err != nil {
		t.Fatalf("安装测试驱动失败: %v", err)
	}

	if err := manager.RenderWithDebug(collectorA, &strings.Builder{}, "request-a", nil); err != nil {
		t.Fatalf("请求 A 渲染失败: %v", err)
	}
	if _, err := manager.FetchWithDebug(collectorB, "request-b", nil); err != nil {
		t.Fatalf("请求 B 渲染失败: %v", err)
	}
	if err := manager.Render(&strings.Builder{}, "legacy-render", nil); err != nil {
		t.Fatalf("旧 Render 渲染失败: %v", err)
	}
	if _, err := manager.Fetch("legacy-fetch", nil); err != nil {
		t.Fatalf("旧 Fetch 渲染失败: %v", err)
	}
	if err := manager.RenderWithDebug(nil, &strings.Builder{}, "untracked", nil); err != nil {
		t.Fatalf("不采集的显式渲染失败: %v", err)
	}

	if files := debugFiles(collectorA); !reflect.DeepEqual(files, []string{"request-a"}) {
		t.Fatalf("请求 A 模板记录隔离错误: %#v", files)
	}
	if files := debugFiles(collectorB); !reflect.DeepEqual(files, []string{"request-b"}) {
		t.Fatalf("请求 B 模板记录隔离错误: %#v", files)
	}
	if files := debugFiles(legacyCollector); !reflect.DeepEqual(files, []string{"legacy-render", "legacy-fetch"}) {
		t.Fatalf("旧 Render/Fetch 应继续使用构造时 collector，实际为 %#v", files)
	}
}

func debugFiles(collector *debug.Debug) []string {
	return collector.GetInfo()["files"].([]string)
}
