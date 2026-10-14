# 上传：显式内容策略、资源预算与原子发布

**`PutFile` / `PutFileAs` 未提供 `upload_policy` 时默认返回 `ErrUploadRejected`，不会打开或保存上传文件。** 应用处理不可信上传必须使用策略，并限制进入 multipart 解析前的整个 HTTP 请求。已在业务层校验的可信输入可显式传 `map[string]interface{}{"trusted_upload": true}` 保留原有命名和存储能力，此出口不会校验类型或大小；它不能由请求参数直接控制，也不能与 `upload_policy` 同时设置。框架默认 `local` 磁盘位于 `runtime/storage`；公开磁盘是显式选择，private 权限标记不能撤销 Web 服务器已有的目录映射。

## 创建与使用

在应用初始化时创建一次不可变策略，失败应阻止相关上传功能启用。`MaxBytes` 必须为正数且小于 `math.MaxInt64`；`AllowedTypes` 将扩展名映射到允许的**嗅探 MIME 基础类型**，不是分别独立的两个列表。

```go
policy, err := filesystem.NewUploadPolicy(filesystem.UploadPolicyConfig{
    MaxBytes: 4 << 20,
    AllowedTypes: map[string][]string{
        "png":  {"image/png"},
        "jpg":  {"image/jpeg"},
        "jpeg": {"image/jpeg"},
    },
})
if err != nil {
    return err
}

// file 是已经完成身份认证、对象授权和请求限额检查的 multipart.FileHeader。
// disk 应是非 Web 可达的本地磁盘。业务自行持久化并授权返回的相对 key。
key, err := disk.PutFile("images", file, "sha256", map[string]interface{}{
    "upload_policy": policy,
    "visibility": filesystem.VisibilityPrivate,
})
```

指定保存名时，把同一选项传给 `disk.PutFileAs("images", file, "business-id.jpg", options)`。`PutFile` 的参数位置仍是规则、选项；使用随机日期文件名时显式传 `nil` 规则，再传选项。

扩展配置可带一个前导点，大小写不敏感；规范化后重复的扩展名被拒绝。扩展名限 1–32 个 ASCII 字母或数字。MIME 类型不可含通配符或参数，空策略、空 MIME 列表被拒绝。选项存在却传入 nil、零值、非 `*UploadPolicy` 对象，会返回 `ErrInvalidConfiguration`，而不是退回未校验路径。策略深拷贝配置的 map 和 MIME 列表，可并发复用；构造期间不得并发修改配置，调用上传期间也不得并发修改其文件头或选项。

## 实际保证

客户端文件名的最后扩展名和**最终保存名**的扩展名，都必须允许从实际内容嗅探出的 MIME。客户端 `Content-Type` 不作为可信判断；不以 `FileHeader.Size` 作为单文件大小的证明，而是计量 `FileHeader.Open()` 实际提供的字节流。只有两个扩展均配置相同 MIME 时，才能例如将 jpeg 改名为 jpg。

策略路径的最终 basename 必须是非空 ASCII 字母、数字、下划线或连字符组成的 stem，加一个点及扩展，合计不超过 255 字节。这会拒绝双扩展、隐藏名称、空扩展、空格、冒号、百分号等歧义命名；只影响策略路径，不改变通用 `Write` / `WriteStream` API。目录由可信业务指定，仍经原有相对路径与 `os.Root` 边界校验。原始展示名称可作为独立的非可信业务元数据保存，不必用作磁盘名。

策略仅允许 `PutFile` 的随机命名和已有内置哈希规则（md5、sha1、sha256、sha384、sha512）。自定义命名回调在打开源文件前被拒绝，因为框架不能限制任意回调自己的读取或副作用；需要定制名称时用 `PutFileAs`。内置哈希只是命名工具，不是签名、内容安全判定或访问凭据。相同哈希文件名可能覆盖已有对象，应用仍需对象授权和合适的冲突策略。

源流先复制到私有、随机且以 0600 创建的临时快照，最多读取 `MaxBytes + 1` 字节；额外一字节用于检测超限，不会截断后报告成功。随后嗅探、哈希命名及最终写入使用同一快照，不重新打开原始上传源。MIME 检测只检查最多 512 字节；参数被剥离后作基础类型精确比较，CSV 等内容可能被识别成 text/plain，应用应明确选择可接受组合，而不是把客户端声明当结果。

失败返回的策略错误可通过 `errors.Is(err, filesystem.ErrUploadRejected)` 判断，大小和类型分别是 `ErrUploadTooLarge` / `ErrUploadTypeNotAllowed`。原始读取、文件系统或关闭错误保留错误链，不能将全部错误都当作客户端违规。

## 持久化和资源边界

在现有原子写入流程发布目标前，快照必须关闭并删除。内容超限、类型拒绝、读取/复制/源关闭/原子替换失败不会发布新目标或覆盖原目标；临时文件会尝试清理，清理失败会返回错误，不隐瞒泄漏。文件系统故障发生在原子发布之后时，仍沿用 `ErrFilesystemOperationCommitted`：目标可能已经改变，不能盲目重试。发布阶段仍可能留下已创建的空父目录，不承诺整个目录树的事务回滚。

临时快照使用系统临时目录；部署必须保证它非 Web 可达、访问隔离并配置磁盘配额。此策略限制的是单次上传源字节数，不是整个进程、租户或请求的总量限制。multipart 解析可能已经产生临时文件，原子写入还需要目标目录中的临时副本，因此必须另配请求大小、文件数量、并发准入、磁盘总预算与请求超时；调用方负责 `MultipartForm.RemoveAll()`。同步 API 不提供运行中取消或崩溃后自动回收保证。

## 不能据此宣称的保证

通过 MIME 嗅探不证明整个文件符合格式，也不排除多格式文件、恶意尾部、宏、脚本、解压炸弹或恶意内容。图片等内容仍可按业务使用完整解码、重编码和大小/像素预算，必要时隔离扫描。允许 HTML、SVG、JavaScript 或任意 binary MIME 不会因此变安全。本功能没有隐式扩展黑名单；业务不能从不可信请求生成允许列表。

不可信内容优先非 Web 存储，通过授权下载路由或独立来源提供；禁止上传目录脚本执行，按用途设置下载响应头。参考 [OWASP File Upload Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/File_Upload_Cheat_Sheet.html) 的分层防护，以及 [Go DetectContentType](https://pkg.go.dev/net/http#DetectContentType) 的检测范围。

包内测试覆盖两上传入口、实际内存/磁盘 multipart、真实字节边界、原/最终扩展与 MIME 配对、命名与写入一致性、配置快照、并发、错误身份及原子清理。它不是浏览器攻击、病毒扫描、总磁盘配额或故障恢复的端到端认证。
