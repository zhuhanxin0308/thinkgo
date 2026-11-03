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
// 当成可信授权结果。上传 API 未显式传入 [UploadPolicy] 时默认拒绝；
// 可信应用输入可显式设置 trusted_upload: true，此出口不校验上传类型或大小。
// 不可信上传可使用 [NewUploadPolicy] 指定扩展与嗅探 MIME 配对及实际字节上限；
// 仍须独立执行请求总限额、完整内容审查和对象授权，参见 docs/security/upload-policy.md。
//
// # 原子写入目录
//
// Write、Copy 和上传的暂存/发布要求根目录至目标父目录全部为真实目录。
// Unix 下还要求目录归进程有效 UID 或 root 所有，且组和其他用户没有写权限；
// 不满足时在读取写入内容前返回 [ErrUnsafeLocalWriteDirectory]。
// 这些检查提供 POSIX DAC 边界，不验证 macOS 扩展 ACL、Windows DACL 或关闭所有权
// 检查的文件系统。部署仍须确保这些机制不向不可信用户授予目录修改权限，且同 UID
// 与管理员进程可信。临时文件使用已打开句柄设置权限；检测到临时路径替换时不发布、
// 不删除替换项。完整条件见 docs/security/filesystem-atomic-write.md。
//
// 可执行示例见 [Local.Path]。仓库 docs/security/filesystem-paths.md
// 说明绝对路径、相对 key、公共 URL 与私有下载之间的不同契约。
package filesystem
