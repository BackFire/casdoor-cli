---
name: casdoor
description: 通过 casdoor CLI 管理自建 Casdoor 的用户、角色、权限和应用。用户要求查询账号、启停用户、调整角色成员、分配权限或修改应用配置时使用；Casdoor 部署升级不属于此 Skill。
---

# Casdoor 管理

优先使用已安装的 `casdoor` CLI，通过 API 操作，不直接修改数据库。CLI 源码和详细用法见仓库根目录的 [README](../../README.md)。

## 本机配置

- 可执行文件：`casdoor`；PATH 找不到时检查 `$HOME/.local/bin/casdoor`。
- 配置文件：`$HOME/.config/casdoor/config.json`，权限 `0600`。其中已有 `prod` profile，默认连接生产环境；凭据不在本 Skill 或仓库中。
- 首次在会话中使用，执行 `casdoor config show --profile prod`，确认实际 endpoint 和 owner。用户指定其他环境时选择相应 profile，不能将生产与测试混用。
- 当前生产用户、角色、权限的 owner 为 `chandashi`；应用的 owner 为 `admin`。ID 一律用完整 `OWNER/NAME`，用户名可能是中文或钉钉标识，不要用显示名称代替 ID。
- 应用凭据使用 Basic Auth；CLI 不继承浏览器登录权限。遇到权限拒绝先核对 profile、owner 和目标 ID，不擅自扩大应用权限。

## 查询

先取分页摘要，再按需取详情；不要把全部员工资料输出到上下文。

```sh
casdoor user list --profile prod --owner chandashi --page 1 --page-size 20
casdoor role list --profile prod --owner chandashi --page 1 --page-size 20
casdoor permission list --profile prod --owner chandashi --page 1 --page-size 20
casdoor application list --profile prod --owner admin --page 1 --page-size 20
casdoor user get 'chandashi/USERNAME' --profile prod
casdoor permission by-role 'chandashi/ROLE' --profile prod
```

输出是 JSON：资源在 `.data`，分页总数在 `.total`。成功退出码为 0；失败退出码为 1，stderr 为 JSON 错误。查询默认脱敏，不使用 `--show-secrets`，除非任务确实需要读取凭据。

## 修改

用户明确要求的目标和变更即为本次操作范围；授权明确时执行，不反复确认。安装此 Skill 本身不代表授权修改生产数据。

四类资源均支持 `create --file FILE`、`update OWNER/NAME --file FILE`、`delete OWNER/NAME`。文件可用 `-` 从标准输入读取。写入前用 `--dry-run` 检查目标和请求；确认符合已有授权后去掉该参数执行，并读取对象验证结果。删除先读取目标身份，删除后用查询确认；不要盲目重试不确定是否成功的写请求。

```sh
printf '%s\n' '{"displayName":"新显示名称"}' |
  casdoor user update 'chandashi/USERNAME' --profile prod --file - --dry-run

casdoor role add-user 'chandashi/ROLE' --user 'chandashi/USERNAME' --profile prod --dry-run
casdoor role remove-user 'chandashi/ROLE' --user 'chandashi/USERNAME' --profile prod --dry-run
casdoor permission add-role 'chandashi/PERMISSION' --role 'chandashi/ROLE' --profile prod --dry-run
casdoor permission remove-role 'chandashi/PERMISSION' --role 'chandashi/ROLE' --profile prod --dry-run
```

- update 输入是顶层字段补丁，CLI 先 GET 再合并，保留 ARN 等定制字段。数组、对象整体替换；不要把默认脱敏的查询输出整份回写。
- GET → POST 不是原子操作，同一对象串行修改。角色/权限成员变更优先使用 add-user/remove-user/add-role/remove-role，避免手工覆盖整个成员数组。
- 旧版用户 update 默认仅保存服务端指定列。必要时用 `--columns display_name,is_forbidden` 指定 DB 列；服务端仍会检查权限。通用 update 不支持改 owner/name 或密码。
- 旧版分页接口才支持 `--field/--value` 和排序；筛选字段用 DB 列名，如 `display_name`。`application list --organization ORG --owner admin` 不能同时分页。
- `.affected: false` 表示服务端返回 Unaffected，不要声称已经修改。权限/登录是否实际生效，需要相应访问验证，读取到配置不等于登录流程已通过。
- 新建资源模板和其他参数按需查看仓库 `examples/`、README 或 `casdoor --help`。本地模拟写测试和生产只读查询已通过；不能声称所有生产写路径已做过真实测试。
