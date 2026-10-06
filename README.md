# dst-admin-go (AprEcho Fork)
> 《饥荒联机版》服务器管理后台 —— 本仓库为 **AprEcho** 的 Fork 修改定制版。
> 
> 上游原项目地址: [hujinbo23/dst-admin-go](https://github.com/hujinbo23/dst-admin-go)

---

## 📌 本 Fork 修改特性

本分支针对原版 Docker 部署卷挂载繁琐、初始引导设计粗糙、路径割裂等痛点进行了深度重构与精简：

1. **统一数据持久化存储 (`./data`)**
   - 面板所有持久化数据（数据库 `dst-db`、配置 `dst_config`、账号密码 `password.txt`、备份目录 `backup`、模组目录 `mods`）全部统一归拢于 `/app/data`。
   - 部署时仅需挂载单卷 `- ./data:/app/data`，彻底告别原版 6~7 个分散单文件与目录挂载的繁琐。
2. **彻底移除 `first` 标记文件，优化鉴权机制**
   - 废除原版脆弱的 `first` 空文件标记机制。
   - 系统基于 `data/password.txt`（及数据库）智能判断是否已初始化。容器启动自动写入默认凭据（`admin` / `123456`），开箱即用；忘记密码时可随时直接编辑宿主机 `data/password.txt` 离线改密。
3. **规范模组路径与 UGC 目录动态推导**
   - 模组缓存目录规范命名为 `mods` (`/app/data/mods`)。
   - 游戏服务器启动参数 `-ugc_directory` 严格由模组路径动态生成（`<mod_download_path>/steamapps/workshop`），彻底杜绝原版静态配置不同步导致模组无法加载的问题。
4. **清理冗余历史兼容包袱**
   - 移除启动脚本中多余的软链接兼容（如 `/app/backup`、`/app/mod` 等）与旧数据字符串替换补丁，保持代码轻量纯粹。
5. **内置官方 Docker Compose 模板**
   - 根目录内置标准 `docker-compose.yml`，一键拉取镜像即可启动。

---

## 🚀 快速开始 (Docker Compose)

### 1. 获取 `docker-compose.yml`

仓库根目录下已内置 `docker-compose.yml`，内容如下：

```yaml
services:
  dst-admin-go:
    image: ghcr.io/aprecho/dst-admin-go:latest
    container_name: dst-admin-go
    restart: always
    network_mode: host
    # 如需使用桥接网络映射端口，请注释 network_mode 并取消以下端口注释：
    # ports:
    #   - "8082:8082/tcp"
    #   - "10888:10888/udp"
    #   - "10998:10998/udp"
    #   - "10999:10999/udp"
    volumes:
      - /usr/share/zoneinfo/Asia/Shanghai:/etc/localtime:ro
      - /etc/timezone:/etc/timezone:ro
      - ./steamcmd:/app/steamcmd
      - ./DoNotStarveTogether:/root/.klei/DoNotStarveTogether
      - ./dst-dedicated-server:/app/dst-dedicated-server
      - ./data:/app/data
```

### 2. 启动服务

```bash
docker compose up -d
```

### 3. 访问面板与初始账号

- 打开浏览器访问：`http://<服务器IP>:8082`
- **默认管理员账号**：`admin`
- **默认管理员密码**：`123456`
- **忘记密码**：直接编辑宿主机 `./data/password.txt` 文件中的 `password=`，保存即可实时生效。

### 4. 挂载持久化目录结构

单卷挂载 `./data:/app/data` 后，宿主机的 `./data` 目录结构如下：

```text
./data/
├── backup/         # 游戏存档备份与快照目录
├── mods/           # 创意工坊模组下载与缓存目录 (UGC: mods/steamapps/workshop)
├── dst-db          # SQLite 数据库文件
├── dst_config      # 平台与服务器配置参数 (自动生成与管理)
└── password.txt    # 管理员登录凭据 (可直接编辑修改)
```

---

## 项目简介

**现已支持 Windows 和 Linux 平台**

DST Admin Go 是一个使用 Go 语言开发的《饥荒联机版》服务器管理面板，具有以下特点：

- 🚀 **部署简单**：单个可执行文件，无需复杂配置，开箱即用
- 💾 **资源占用低**：基于 Go 语言开发，内存占用小，运行高效
- 🎨 **界面美观**：现代化的 Web 界面，操作直观友好
- ⚙️ **功能完善**：
  - 可视化配置游戏房间和世界参数
  - 在线管理和配置 Mod（模组）
  - 支持多个集群（Cluster）和世界的统一管理
  - 游戏存档备份与快照恢复
  - 玩家管理（白名单、黑名单、管理员）
  - 实时日志查看和游戏控制台
  - 游戏服务器自动更新检测

## 预览

![首页效果](docs/image/dashboard.png)
![首页效果](docs/image/panel.png)
![首页效果](docs/image/toomanyitemplus.png)
![首页效果](docs/image/player.png)
![房间效果](docs/image/home.png)
![世界效果](docs/image/level.png)
![世界效果](docs/image/selectormod.png)
![模组效果](docs/image/mod1.png)
![模组效果](docs/image/mod3.png)
![模组效果](docs/image/mod2.png)
![日志效果](docs/image/playerlog.png)
![大厅效果](docs/image/lobby.png)

---

## 本地编译构建

### Linux 打包

```bash
bash scripts/build_linux.sh
# 输出: dst-admin-go (Linux amd64 二进制文件)
```

### Windows 打包

```bash
bash scripts/build_window.sh
# 输出: dst-admin-go.exe (Windows amd64 二进制文件)
```

### 交叉编译 Linux 二进制

```cmd
set GOARCH=amd64
set GOOS=linux
go build -o dst-admin-go cmd/server/main.go
```
