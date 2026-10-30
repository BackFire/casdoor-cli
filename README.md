# casdoor-cli

面向自建旧版 Casdoor 的 Go CLI，支持用户、角色、权限、应用管理。单个二进制、无第三方运行依赖，默认 JSON 输出，适合人工和 AI Agent 调用。

## 安装

需要 Go 1.23+：

```sh
git clone git@github.com:BackFire/casdoor-cli.git
cd casdoor-cli
go build -trimpath -o bin/casdoor .
./bin/casdoor --help
```

可将 `bin/casdoor` 安装到 PATH 中。运行二进制不需要 Go。

## 认证与配置

在 Casdoor 管理后台「身份认证 → 应用」中查看 Client ID / Client Secret。使用应用凭据进行 HTTP Basic Auth，不需要浏览器登录态；访问范围由服务端给该应用授予的权限决定。生产旧版已实测 Basic Auth，新版可使用 Bearer Token；旧版 Bearer 支持取决于其实现。

配置文件默认是 `$XDG_CONFIG_HOME/casdoor/config.json`，未设置 XDG 时为 `~/.config/casdoor/config.json`：

```sh
mkdir -p ~/.config/casdoor
chmod 700 ~/.config/casdoor
install -m 600 examples/config.json ~/.config/casdoor/config.json
# 编辑该文件，填写实际地址、组织和凭据
casdoor config show
```

支持多 profile，格式见 [examples/config.json](examples/config.json)。追加 `--profile test` 可选择其他 profile。配置文件必须禁止组和其他用户访问，例如权限 `0600`；不要将真实配置提交到仓库。

也可以只使用环境变量（通过自己的密钥管理器注入）：

```text
CASDOOR_ENDPOINT
CASDOOR_CLIENT_ID
CASDOOR_CLIENT_SECRET
CASDOOR_TOKEN          # 可选，配置后优先使用 Bearer Auth
CASDOOR_OWNER
CASDOOR_CONFIG        # 可选，配置文件路径
CASDOOR_PROFILE       # 可选，profile 名称
```

优先级：命令行 `--endpoint/--owner` > 非空环境变量 > profile。profile 选择优先级：`--profile` > `CASDOOR_PROFILE` > `defaultProfile`。文件路径优先级：`--config` > `CASDOOR_CONFIG` > 默认路径。没有凭据命令行参数，避免凭据进入进程参数列表。

服务地址必须使用 HTTPS（本机回环地址允许 HTTP）；不会跟随重定向，不会自动重试写请求。网关若有路径前缀，endpoint 包含此前缀，但不包含 `/api`。

## 命令

四类资源均支持相同的增删改查：

```sh
casdoor user list --owner example-org --page 1 --page-size 20
casdoor user get example-org/alice
casdoor user create --file examples/user.json --dry-run
casdoor user create --file examples/user.json
printf '%s\n' '{"displayName":"Alice Zhang","isForbidden":false}' |
  casdoor user update example-org/alice --file - --dry-run
casdoor user delete example-org/alice

casdoor role list --owner example-org
casdoor role get example-org/readers
casdoor role create --file examples/role.json
casdoor role update example-org/readers --file role-patch.json
casdoor role delete example-org/readers

casdoor permission list --owner example-org
casdoor permission get example-org/read-documents
casdoor permission create --file examples/permission.json --dry-run
casdoor permission update example-org/read-documents --file permission-patch.json
casdoor permission delete example-org/read-documents

casdoor application list --owner admin
casdoor application get admin/example-app
casdoor application create --file examples/application.json
casdoor application update admin/example-app --file application-patch.json
casdoor application delete admin/example-app
```

`permission.json` 中的 model、adapter、resourceType 是占位符，必须改为服务端已有的有效配置。应用示例仅创建基本配置，不会自动配置证书、钉钉或下游应用。创建时没有指定 `createdTime`，CLI 自动填入当前 RFC3339 时间。用户密码设置不属于通用 update 的支持范围。

### 角色与权限成员

```sh
casdoor role add-user example-org/readers --user example-org/alice
casdoor role remove-user example-org/readers --user example-org/alice
casdoor role add-role example-org/readers --role example-org/child-role
casdoor role remove-role example-org/readers --role example-org/child-role

casdoor permission add-user example-org/read-documents --user example-org/alice
casdoor permission remove-user example-org/read-documents --user example-org/alice
casdoor permission add-role example-org/read-documents --role example-org/readers
casdoor permission remove-role example-org/read-documents --role example-org/readers
casdoor permission by-role example-org/readers
```

上述命令通过 GET → 更新成员数组 → POST update 实现。重复添加不会重复插入成员，移除不存在的成员不会改变其他成员。全部支持 `--dry-run`。

### 查询与更新语义

- ID 必须是完整 `OWNER/NAME`，支持中文，自动进行 URL 编码。用户的 owner 是组织；应用的 owner 通常为 `admin`；角色、权限的 owner 以实际部署为准。profile 的 owner 仅用于列表，不会重写请求中的 ID。
- `list` 不指定分页时返回服务端完整列表；大组织建议分页。分页必须同时传 `--page` 和 `--page-size`，不会隐式遍历所有页。
- `--field/--value` 和 `--sort-field/--sort-order` 仅用于分页列表，这是旧版接口限制。字段名使用服务端 DB 字段，如 `display_name`，排序方向为 `ascend` 或 `descend`。
- `application list --organization example-org --owner admin` 可按组织过滤无分页列表。旧版分页分支忽略 organization，因此 CLI 拒绝将此过滤器与分页组合。
- `update` 的文件是**顶层字段补丁**：先 GET，保留服务端字段，再用输入字段覆盖。数组和对象整体替换；输入 `null` 按 JSON null 传给服务端，不是删除字段。不会将响应映射到精简 SDK 模型，因此能保留 ARN、provider、platform、`third_role_Id` 等定制字段及大整数。
- 旧版 update 通常是整行覆盖。GET 和 POST 不是原子操作，没有 ETag/版本条件；同一对象应串行修改，避免覆盖并发更改。
- 旧版用户 update 默认只保存服务端指定的列。必要时用 `--columns display_name,is_forbidden` 显式指定 DB 列。是否可更新仍受服务端权限和校验约束；不能用此参数绕过权限。不支持通过通用 update 修改 owner/name、密码。
- `delete` 是明确的写命令，直接执行，无交互确认。先追加 `--dry-run` 可以预览请求。不会自动删除依赖或执行级联清理，具体关联处理由服务端决定。

### 输出与 AI 使用

成功输出始终包含 `data`。分页额外包含 `total`；旧版写接口的 `Affected/Unaffected` 映射为 `affected: true/false`，没有变化不会伪装为已修改。失败退出码为 `1`，stderr 输出 `{"error":{"message":"..."}}`；成功退出码为 `0`。

```sh
casdoor user list --owner example-org --page 1 --page-size 20 |
  jq '.data[] | {owner, name, displayName}'
casdoor application get admin/example-app | jq '.data.redirectUris'
```

密码、clientSecret、token 等字段默认递归脱敏。确实需要导出凭据时显式加 `--show-secrets`；此选项不影响 `config show`，后者始终只输出配置摘要。脱敏输出用于查看，不要把脱敏后的整个对象作为 update 输入；提交包含实际变更的补丁即可。AI 使用方式是调用本 CLI，不依赖 MCP。

```sh
printf '%s\n' '{"displayName":"Readers"}' |
  casdoor role update example-org/readers --file - --dry-run
# 确认预览符合任务授权后，去掉 --dry-run 执行同一命令
```

## 验证与兼容范围

已针对生产自建旧版验证四类资源的分页列表和单条查询，认证使用 Basic Auth。未在生产执行写测试。更新、创建、删除、成员管理、错误响应、脱敏、身份保护和未知字段保留，由本地 HTTP 模拟服务测试覆盖；官方新版的整套兼容性尚未验证。

```sh
go test -race -cover ./...
go vet ./...
go build ./...
```

CI 执行相同的测试、静态检查和构建，不需要任何生产凭据。
