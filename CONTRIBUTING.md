# Contributing

[English](CONTRIBUTING.md) | [简体中文](CONTRIBUTING.zh-CN.md)

Thanks for considering a contribution.

## Before you start

- This repository is currently a **design/spec** repo. Code contributions should follow the contracts in `docs/`.
- For a significant change, open an issue first and describe the problem, the proposed contract, and the migration impact.
- Do not include credentials, tenant data, private endpoints, or internal documents.

## Pull requests

1. Fork and create a topic branch.
2. Keep changes focused; one concern per PR.
3. Update docs/contracts if behavior changes.
4. Add tests when code is added.
5. Run the documented checks locally.

## Commit style

Use a clear imperative subject. Example:

```text
docs: clarify branch refs and workstream relationship
feat: add S3 multipart adapter
fix: reject expired writer fencing token
```

## License

By contributing, you agree that your contribution is licensed under Apache-2.0.

## Security

Do not open a public issue for a vulnerability. See [SECURITY.md](SECURITY.md).
