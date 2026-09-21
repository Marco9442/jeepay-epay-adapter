# 镜像发布

发新版先改仓库根目录的 `VERSION`（必须是 `x.y.z`），再合进 `main`。GitHub Actions 会打 git 标签 `vX.Y.Z`，并并行做两件事：在 GitHub 构建并推 GHCR；把同一份代码推到 CNB，再触发 CNB 构建。两边都只打这一个版本标签，不打 `latest`，也不用提交号。源码相同，不必对镜像 digest。

同一版号不能对应两次不同的提交：`VERSION` 没改就合进 `main`，发布会失败。重跑同一提交会跳过。

- GHCR：`ghcr.io/marco9442/jeepay-epay-adapter:v0.1.0`
- CNB：`docker.cnb.cool/baorui.xyz/jeepay-epay-adapter:v0.1.0`

国内机器优先拉 CNB。生产 compose 默认钉 GHCR 的当前版本；换国内源时把 `ADAPTER_IMAGE` 换成上面的 CNB 地址，标签保持同一个 `vX.Y.Z`。

CNB 仓库是 `baorui.xyz/jeepay-epay-adapter`。GitHub 用 Secret `CNB_TOKEN` 推代码、调构建接口，仓库变量 `CNB_REPO` 指向它。Token 只放 Secret，不进 Git。

PR 只跑 `go test` 和检查 `VERSION` 格式，不发镜像。需要重发当前提交时，在 Actions 里手动跑「发布镜像」（版本已存在则跳过）。
