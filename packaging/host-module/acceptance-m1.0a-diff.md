# MFGraph M1.0a 验收差异表（预填）

> MFGraph 0.1.0（M1.0a）作为 AgentTeams 模块运行时的差异表，格式同宿主附录 B。
> “实测”一列由 MFGraph 预填：`scripts/acceptance-m1.0a.sh` 在 MySQL 8.0 和 seekdb 上、通过 UDS 实际跑通的结果（MFGraph CI 每次构建都在 MySQL 8.0 上用 bundle 跑一遍）。
> 宿主在自己的环境（seekdb + UDS）跑同一个脚本，脚本最后会打印同样格式、带本次实测值的表，用来回填报告。

| # | 项 | 期望（契约） | 实测（MFGraph 预填） | 偏差/处理 |
|---|---|---|---|---|
| 1 | 启动/readyz | readyz=ready；UDS 0600；无 TCP | readyz=ready（db、storage、outbox 三项都是 ok）；socket 权限 0600；只监听 `MODULE_SOCKET`/`--socket` 给的 UDS，没有 TCP 端口；残留的旧 socket 会被清掉 | 无。模块模式配了 TCP 地址会拒绝启动 |
| 2 | 前缀 | `/internal/v1/mfgraph` 可达；`/api/v1` 404 | 在 `/internal/v1/mfgraph` 下完成建工作区 → 上传（新路由 `/objects/{workspace_id}`）→ 提交 → 列目录 → 下载（sha256 一致）→ 恢复；`/api/v1/...` 返回 404 | 无 |
| 3 | 身份 | 无 token/错 token 拒绝；claims 头可取 actor | 无 token、错 token、缺 `X-Atf-Actor`/`X-Atf-Actor-Type`/`X-Atf-Project`、actor 不是 UTF-8：都是 401；快照的 `author_ref`/`author_type` 等于头里的 actor 和类型 | 无。直连模块时由脚本自己加 claims 头；经宿主转发时由 kernel 注入 |
| 4 | ACL | 跨 project 资源 404 | 用另一个 `X-Atf-Project` 访问工作区、分支、快照、目录、文件和上传：都是 404，和“ID 不存在”的响应相同；`/projects` 404 | 无。模块不返回 403 |
| 5 | 上传限额 | 新路由 1GiB/600s 生效；旧路由 8MiB | 新路由 `PUT /internal/v1/mfgraph/objects/{workspace_id}` 可以上传，行为和旧路由完全相同；模块自己的上限是 1 GiB（`MFGRAPH_MAX_UPLOAD_BYTES`） | 限额由宿主代理按前缀执行，由宿主实测 |
| 6 | 迁移 | `migrate` 幂等；版本记录；--config 生效 | `bin/mfgraph-server --config etc/module.env migrate` 连跑两次都成功，第二次全部跳过；`schema_migrations` 只有 `mfgraph` 组件的 7 个版本（0001–0009，编号有空缺），校验和等于 bundle 里 `migrations/` 的文件；没有建 kernel 表（`projects`、`principals`），说明 `--config` 里的模块模式生效 | 无 |
| 7 | 事件 | M1.0a 六类事件；envelope 字段齐；event_id 稳定 | `ops.module_outbox` 里按顺序出现 workspace_created 1、branch_created 1、commit_created 2、snapshot_restored 1、branch_updated 3（reason 依次为 commit、commit、restore）、snapshot_created 0；`module=mfgraph`、`scope_kind=project`、`scope_id`=项目、`actor_type`/`actor_id` 为完整值；envelope 的 v、event_id、occurred_at、project_id、actor 齐全；event_id 等于 `docs/events.md` 写的派生算法；事件名都在 `docs/events.md` 里 | 六类登记、五类实发，原因：M1.0a 没有手动快照或恢复前快照接口。`mfgraph.snapshot_created` 在 M1.0b 引入显式快照时启用 |
| 8 | 退出 | SIGTERM ≤8s；重启数据在 | 收到 SIGTERM 后 1 秒内正常退出（退出码 0），socket 被删除；重启后目录和恢复出来的文件都在 | 无。没投递出去的事件留在本地 outbox，重启后补发 |
| 9 | 独立形态 | `mfgraph-server` 无宿主可跑 | 同一个二进制不带任何宿主变量即可运行；独立形态的验收测试在 MFGraph CI 里（MySQL 8.0、seekdb）全部通过 | 无 |
