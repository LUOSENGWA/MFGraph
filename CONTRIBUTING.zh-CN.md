# 贡献

[English](CONTRIBUTING.md) | **简体中文**

感谢考虑贡献。

## 开始之前

- 本仓库目前是**设计/规格**仓库。代码贡献应遵循 `docs/` 中的契约。
- 重大变更请先开 issue，说明问题、拟议契约和迁移影响。
- 不要包含凭据、租户数据、私有端点或内部文档。

## Pull Request

1. Fork 并创建 topic branch。
2. 保持改动聚焦；一个 PR 只解决一个问题。
3. 行为变化时同步更新 docs/contracts。
4. 添加代码时补充测试。
5. 本地跑文档检查：

   ```bash
   python3 scripts/check_links.py .
   python3 scripts/scan_sensitive.py .
   ```

## Commit 风格

使用清晰的祈使句 subject。示例：

```text
docs: clarify branch refs and workstream relationship
feat: add S3 multipart adapter
fix: reject expired writer fencing token
```

## 许可

提交贡献即表示你同意以 Apache-2.0 许可你的贡献。

## 安全

漏洞不要开公开 issue。见 [SECURITY.zh-CN.md](SECURITY.zh-CN.md)。
