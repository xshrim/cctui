# CC Switch TUI

`CC Switch TUI` 是一个终端界面工具，用来管理并切换 `Claude`、`Codex`、`Gemini`、`Opencode` 的多套供应商配置。

它适合以下场景：

- 在官方接口、代理接口、公司内网网关之间快速切换
- 为不同应用分别维护多套 `Base URL`、`API Key`、`Model`
- 用可视化 TUI 替代手动编辑多个配置文件

## 功能特性

- 支持 `Claude`、`Codex`、`Gemini`、`Opencode` 四类应用
- `Claude`、`Codex`、`Gemini` 为替换模式：同一时刻只有一个供应商生效
- `Opencode` 为增量模式 `(I)`：所有供应商共存于同一配置文件
- 使用 SQLite 保存供应商配置，数据默认位于 `~/.cc-switch/`
- 首次启动时，如果某个应用还没有保存的供应商，会尝试导入当前 live 配置
- 切换前会先读取当前 live 配置并回写数据库，尽量保留你在外部手动改过的内容
- 切换前会检测 live 配置是否被外部修改；发现冲突时会取消切换，避免静默覆盖
- 支持新增、编辑、删除、切换供应商
- `Codex` 额外支持配置 `Reasoning Effort`
- 切换 `Codex` 供应商时可选择自动恢复历史会话

## 管理的配置文件

程序会读取并写入这些 live 配置文件：

- `Claude`：`~/.claude/settings.json`
- `Claude` 兼容旧文件：`~/.claude/claude.json`
- `Codex`：`~/.codex/auth.json`
- `Codex`：`~/.codex/config.toml`
- `Gemini`：`~/.gemini/.env`
- `Gemini`：`~/.gemini/settings.json`
- `Opencode`：`~/.config/opencode/opencode.json`（或 `opencode.jsonc`）

程序自己的本地数据默认保存在：

- `~/.cc-switch/cc-switch.db`
- `~/.cc-switch/settings.json`

## 环境要求

- `Go 1.26+`
- 可以访问对应应用的本地配置目录
- 终端支持 TUI/Alt Screen

## 构建与运行

### 直接运行

```bash
go run .
```

### 构建二进制

```bash
go build -o cctui .
./cctui
```

## AUR 自动发布

仓库内置了 GitHub Actions workflow：当你给 GitHub 仓库 push 一个 tag 时，会自动更新 AUR 仓库：

- AUR 仓库地址：`aur@aur.archlinux.org:cctui.git`
- Workflow 文件：`.github/workflows/publish-aur.yml`
- 生成脚本：`scripts/update-aur.sh`

### GitHub Secret

你需要在 GitHub 仓库里配置一个 secret：

- `AUR_SSH_PRIVATE_KEY`

它应该对应一个有权限 push 到 `aur@aur.archlinux.org:cctui.git` 的 SSH 私钥。

### 本地测试 AUR 生成

你已经在本地准备了测试仓库 `~/test/cctui`，可以这样测试：

```bash
./scripts/update-aur.sh \
  --tag v0.0.0 \
  --aur-dir ~/test/cctui \
  --archive-url https://github.com/manateelazycat/cctui/archive/refs/heads/main.tar.gz \
  --archive-dir-name cctui-main \
  --validate
```

这条命令会：

- 生成 `PKGBUILD`
- 生成 `.SRCINFO`
- 用 `makepkg --printsrcinfo` 校验 `.SRCINFO` 是否正确

## 使用方式

启动后会看到按应用分组的供应商列表：

- `Enter`：替换模式下先预览配置 Diff，确认后才切换；增量模式下进入编辑
- 预览页面：`↑/↓` 或 `j/k` 滚动 Diff，`Enter` / `y` 确认切换，`q` / `Esc` 取消
- 切换 `Codex` 时：`r` / `Space` 开关会话恢复，`Enter` / `y` 确认并按当前选项执行，`n` 仅切换不恢复
- `a`：新增供应商
- `e`：编辑供应商
- `d`：删除供应商
- `↑/↓` 或 `j/k`：移动光标
- `1/2/3/4`：快速跳转到 `Claude` / `Codex` / `Gemini` / `Opencode`
- `g/G`：跳到顶部 / 底部
- `q`：退出

表单模式下：

- `Tab` / `Shift+Tab`：切换字段
- `Enter`：下一项，最后一项时保存
- `Ctrl+S`：保存
- `q`：取消并返回

## 供应商字段说明

通用字段：

- `名称`：供应商显示名称
- `Base URL`：接口地址；留空通常表示沿用官方登录或 OAuth 语义
- `API Key`：接口密钥；留空时会尽量保留登录态语义
- `Model`：默认模型
- `Website`：可选，供应商官网
- `Notes`：可选，备注

`Codex` 独有字段：

- `Reasoning Effort`：例如 `medium`、`high`

## 默认行为

### 首次启动导入

如果某个应用在数据库里还没有供应商，程序会尝试读取该应用当前正在使用的 live 配置，并自动导入一条记录，例如 `Imported Claude`。

### 切换时同步

切换到新供应商前，程序会先尝试读取当前 live 配置，并回写到当前供应商记录中；随后再把目标供应商写入 live 配置文件。

切换前会显示将要写入的配置文件和 Diff。API Key、Token、密码等敏感值会脱敏显示；只有确认后才会执行切换。

### Codex 会话恢复

切换 `Codex` 供应商时，TUI 默认打开会话恢复选项。确认后会读取 `~/.codex/state_5.sqlite`（或 `~/.codex/sqlite/state_5.sqlite`），将其他 provider 的未归档会话迁移到当前 provider，并同步修复 rollout JSONL 与 `session_index.jsonl`。修改前会自动创建带时间戳的 `.bak.session-restore-*` 备份；找不到 Codex 会话数据库时不会影响供应商切换，只会在状态栏提示恢复失败。

恢复会话时会校验 rollout 路径位于 Codex 会话目录内，并校验 rollout 的线程 ID；保留原有归档和用户事件状态，session index 使用临时文件原子替换。

### OpenCode 增量模式

OpenCode 的每个 provider 按 `opencode.json` 中真实的 provider key 独立保存，不使用当前供应商状态。新增、编辑、删除 provider 时会保留其他 provider、额外模型及 provider-level 字段。

## 高级配置

可以在 `~/.cc-switch/settings.json` 中覆盖默认配置目录：

```json
{
  "claudeConfigDir": "/path/to/.claude",
  "codexConfigDir": "/path/to/.codex",
  "geminiConfigDir": "/path/to/.gemini",
  "opencodeConfigDir": "/path/to/.config/opencode"
}
```

其中“当前供应商”相关字段也会保存在这个文件里，通常不建议手动修改。

如果对应 CLI 设置了原生配置路径环境变量，cctui 会优先跟随 CLI 的实际路径：

- `Claude`：`CLAUDE_CONFIG_DIR`
- `Codex`：`CODEX_HOME`
- `Gemini`：`GEMINI_CLI_HOME`
- `Opencode`：`OPENCODE_CONFIG`（完整配置文件）或 `OPENCODE_CONFIG_DIR`（配置目录）
- Linux 下未设置 OpenCode 专用变量时，也会识别 `XDG_CONFIG_HOME/opencode`

原生环境变量优先于 cctui 的目录覆盖设置，避免修改到 CLI 实际不会读取的文件。

## 适用场景

- 在官方和第三方中转之间快速切换
- 分离工作环境与个人环境配置
- 为不同模型服务保留独立的历史配置
- 用统一入口管理多个 AI CLI / 桌面工具的连接参数
