# 补丁清单

`dev` 相对上游 `OpenListTeam/OpenList` 的改动，以及每项在上游的状态。同步上游时逐项核对：上游已修的补丁在合并时丢掉，其余保留。

CI、发布、upstream-watch 相关提交（#1、#10、#11）不在此列。

| fork PR | 内容 | 类型 | 上游状态 |
|---|---|---|---|
| #2 | S3 DeleteObject/DeleteObjects 吞掉 `fs.Remove` 的错误 | 修复 | 未修；不提上游；修复分支 `upstream-pr/s3-delete-errors` |
| #3 | 持久化对象 ETag 与元数据（`x_s3_object_meta` 表），HEAD/GET/List 返回 ETag | fork 功能 | 不提：加了数据库表，属于功能取舍。上游类似 PR #3219、#3302、#3350 均已关闭 |
| #4 | PUT 后立刻 HEAD/GET 返回 404（存储列表滞后）；115 Open 查找时重试 | 修复 | 上游已有 issue OpenListTeam/OpenList#3207，修复 PR OpenListTeam/OpenList#3208（open，op 层方案）。我们的实现依赖 #3 的表，不单独提 |
| #5 | 目录映射为 `dir/` marker，去掉 `ThisIsAnEmptyFolderInTheS3Bucket` | 修复 | 上游已有 issue OpenListTeam/OpenList#3186，修复 PR OpenListTeam/OpenList#3303（open）。我们的实现依赖 #3 的表，不单独提 |
| #5（部分） | DELETE `dir` / `dir/` 递归删掉整个目录（数据丢失） | 修复 | 未修（上游 main 上测试复现）；不提上游；最小修复分支 `upstream-pr/s3-delete-dir-key`。#3303 只覆盖没有 marker 的 `dir/`（读代码判断，未实测） |
| #7 | `s3.disable_redirect`：始终代理对象数据，不 302 | fork 功能 | 不提：部署取舍 |
| #8、#12 | 115 Open 上传同名文件不覆盖；按 ID 删除旧文件 | 修复 | 未修；不提上游；修复分支 `upstream-pr/115-open-overwrite`（两个提交合并为一个） |
| #9 | gofakes3 换成 `panghuli-studio/gofakes3` `v0.8.2-panghuli.1`：CreateMultipartUpload 根元素改为 `InitiateMultipartUploadResult` | 修复（依赖） | 未修：gofakes3 v0.8.3 只更新了 `ca-certificates.crt`。OpenListTeam/gofakes3 关闭了 issue，不提上游；修复分支在 `panghuli-studio/gofakes3` 的 `upstream-pr/initiate-multipart-upload-result`。上游修好并升级前保留 go.mod 里的 replace |

2026-11-25 决定这几项不向上游提 issue 或 PR（当时 `gh issue create` 被 GitHub 拒绝，原因未查明）。修复分支保留在 fork 上；同步上游时检查上游是否已自行修复，修了就丢掉对应补丁。
