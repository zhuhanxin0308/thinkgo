// Package filesystem 提供具名磁盘、本地文件与上传存储能力。
//
// # 文件引用与 HTTP 响应
//
// [Local.Path] 返回供进程内 OS 调用使用的绝对路径，包括部署目录信息。
// 不应把它或含内部路径的原始错误直接写入 HTTP、JSON 或前端日志。
// [Local.PutFile]、[Local.PutFileAs] 返回磁盘内的相对 key，可作为业务存储引用；
// 对已决定公开的文件，可通过配置了公共 URL 前缀的 [Local.URL] 构造编码后的 URL。
// URL 不证明文件存在、不发布文件，也不检查对象访问权限。
//
// 私有文件应使用不含服务器路径的业务 ID，通过执行对象级授权的下载路由提供访问。
// VisibilityPrivate 只涉及磁盘权限，不会覆盖 Web 服务器的静态目录发布配置。
// 相对 key 仍可能包含业务敏感信息，且不是访问凭据。不要将客户端原始文件名或 key
// 当成可信授权结果，也不要把存储 API 当作上传内容、MIME 或大小的校验器。
//
// 可执行示例见 [Local.Path]。仓库 docs/security/filesystem-paths.md
// 说明绝对路径、相对 key、公共 URL 与私有下载之间的不同契约。
package filesystem
