# 镜像发布

`main` 有新提交（包括 PR 合并）时，GitHub Actions 并行做两件事：在 GitHub 构建并推 GHCR；把同一份代码推到 CNB，再触发 CNB 构建。两边源码一致，不必对镜像 digest。

- GHCR：`ghcr.io/marco9442/jeepay-epay-adapter:latest`，另打 `sha-<短提交>`。
- CNB：`docker.cnb.cool/baorui.xyz/jeepay-epay-adapter:latest`，另打短提交号。

CNB 仓库是 `baorui.xyz/jeepay-epay-adapter`。GitHub 用 Secret `CNB_TOKEN` 推代码、调构建接口，仓库变量 `CNB_REPO` 指向它。Token 只放 Secret，不进 Git。

PR 只跑 `go test`，不发镜像。需要重发时，在 Actions 里手动跑「发布镜像」。
