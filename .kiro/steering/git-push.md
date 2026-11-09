# 代码完成后推送到 GitHub

远程仓库：`origin` = https://github.com/365code365/huanyou.git（私有），分支 `main`。

每次代码修改或优化完成、并且验证通过（编译 / vet / 测试或实际运行检查）之后，必须提交并推送：

1. `git status` 查看改动，只 `git add` 本次任务改过的文件（按文件名添加，不用 `git add -A` / `git add .`）。工作区里不是本次任务产生的改动保持原样，不要顺手提交，在回复里说明有哪些没提交。
2. 提交前确认没有敏感信息：`doc/`、`.backup/`、`.shots/`、证书和签名文件（`*.jks` `*.p12` `*.mobileprovision` `key.properties`）、服务器密码、私钥、`.env`、数据库文件、编译产物（`bin/`、`data/`）。这些已在 `.gitignore` 里，不要用 `-f` 强制加入。
3. 提交信息用中文，沿用仓库风格：一行概括改了什么、为什么，多项改动用分号隔开。
4. `git push origin main`。如果远程有新提交，先 `git pull --rebase origin main` 再推。
5. 禁止 `git push --force`、`git reset --hard`、改写已推送的历史，除非用户明确要求。推送失败（认证、冲突）时停下来告诉用户原因，不要绕过。
6. 回复里写上提交号（短 hash）和推送结果。

只是回答问题、没有改代码时不需要提交。
