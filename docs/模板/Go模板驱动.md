# Go 模板驱动

默认驱动 `driver.NewGoTemplate()` 基于 `html/template`，因此 `{{ .Value }}` 等 HTML 输出会自动转义。模板文件使用 `view_path` 作为根目录，默认后缀为 `html`。

## 配置

```json
{
    "view_path": "view",
    "view_suffix": "html",
    "cache": true
}
```

应用配置中的相对值 `view` 会解析为当前应用的 `app/<应用名>/view`；需要使用其它目录时应传入项目根目录内的明确路径。

驱动只接受这三个配置键：

| 配置 | 语义 |
|---|---|
| `view_path` | 非空模板根目录；应用初始化会把相对路径按应用根目录解析 |
| `view_suffix` | 后缀，可带一个点，转为小写后只允许字母和数字，最长 16 字符 |
| `cache` | 是否缓存已解析模板，默认 `true` |

未知字段、类型错误、非法后缀会返回 `ErrInvalidViewConfig`。根目录可以延迟到首次访问时才发现不存在；访问失效根目录会返回配置错误，而普通模板缺失会返回模板不存在语义。

## 模板函数

```go
viewManager := app.View()
if err := viewManager.SetFuncMap(map[string]interface{}{
	"upper": strings.ToUpper,
}); err != nil {
	return err
}
```

函数值必须是非 nil 函数，并且满足 `html/template` 的函数约束。注册失败返回 `ErrInvalidTemplateFunction`，不会安装部分函数。

成功更新函数映射会增加驱动 generation 并原子清空旧模板缓存；后续解析使用新的函数集。应用默认注册的 `lang` 函数也遵循这个机制。

## 模板缓存

启用缓存时，同一模板的并发首次解析会合并为一次加载；其它调用等待同一结果。解析成功的模板最多缓存 1024 个，达到上限后新模板仍可渲染但不再进入缓存。缓存没有按文件修改时间自动失效，开发中修改文件后应关闭缓存或重新配置驱动。

`Config`、`SetFuncMap` 会使旧 generation 的缓存失效；并发加载发现 generation 变化时会丢弃旧结果并重新解析。关闭 `cache` 后每次渲染都重新读取和解析模板。
