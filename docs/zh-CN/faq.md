# FAQ

[English](../faq.md) | **简体中文**

## MFGit 是 Git 服务器吗？

不是。它借鉴了内容寻址 snapshot/branch，但不实现 Git 协议、`git clone/push`、rebase 或 cherry-pick。

## 它是 DMS/ECM 吗？

不是。它是版本化底座。元数据、审批、保留策略可以在上层实现；MFGit 不打算替代 M-Files/Alfresco/Nuxeo。

## 它需要 Agent 运行时或 Matrix 吗？

不需要。人、Agent、服务都是 principal。Matrix 与 MCP 是可选适配器。

## 它需要工作流引擎吗？

不需要。它发事件、暴露命令。工作流适配器是可选的；没有它，REST/CLI/MCP 仍可用。

## 能处理大二进制文件吗？

可以。大文件使用内容寻址 chunk 与 packfile；二进制内容用独占锁，而不是自动合并。

## 支持哪些数据库？

v1：MySQL 兼容（MySQL 8.0+、OceanBase MySQL、seekdb 5.7 子集）。PostgreSQL 是后续适配器，不是 v1 门。

## 支持哪些对象存储？

v1 一等：S3 兼容与 POSIX/NFS。Azure Blob 与 GCS 是后续原生适配器。WebDAV/HDFS/CIFS 不是一等。

## 为什么不用 Git LFS？

Git LFS 解决 Git 内的大文件。MFGit 面向通用项目工作区、非代码团队、二进制锁、格式处理器与可选工作流集成。

## 为什么不用 Rust？

Go 是服务/部署/生态适配的主语言。若基准测试需要，Rust 可用于 CPU 密集的 chunk/pack sidecar，而不是核心。

## 许可？

Apache-2.0。依赖必须是 MIT/BSD/Apache/ISC；核心不接受 GPL/AGPL/LGPL/SSPL/BUSL。
