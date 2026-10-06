package handler

import (
	"dst-admin-go/internal/pkg/context"
	"dst-admin-go/internal/pkg/response"
	"dst-admin-go/internal/pkg/utils/dstUtils"
	"dst-admin-go/internal/pkg/utils/fileUtils"
	"dst-admin-go/internal/service/archive"
	"dst-admin-go/internal/service/backup"
	"dst-admin-go/internal/service/dstConfig"
	"dst-admin-go/internal/service/game"
	"dst-admin-go/internal/service/level"
	"dst-admin-go/internal/service/levelConfig"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type PreinstallHandler struct {
	gameProcess      game.Process
	backupService    *backup.BackupService
	archive          *archive.PathResolver
	dstConfig        dstConfig.Config
	levelConfigUtils *levelConfig.LevelConfigUtils
	levelService     *level.LevelService
	lock             sync.Mutex
}

func NewPreinstallHandler(
	gameProcess game.Process,
	backupService *backup.BackupService,
	archive *archive.PathResolver,
	dstConfig dstConfig.Config,
	levelConfigUtils *levelConfig.LevelConfigUtils,
	levelService *level.LevelService,
) *PreinstallHandler {
	return &PreinstallHandler{
		gameProcess:      gameProcess,
		backupService:    backupService,
		archive:          archive,
		dstConfig:        dstConfig,
		levelConfigUtils: levelConfigUtils,
		levelService:     levelService,
	}
}

func (h *PreinstallHandler) RegisterRoute(router *gin.RouterGroup) {
	router.GET("/api/game/preinstall", h.UsePreinstall)
	router.POST("/api/game/preinstall", h.UsePreinstall)
}

// UsePreinstall 应用预设世界模板
// @Summary 应用经典世界模板
// @Description 切换指定的世界模板预设并自动重置房间关卡配置
// @Tags preinstall
// @Accept json
// @Produce json
// @Param name query string true "模板预设名称"
// @Success 200 {object} response.Response
// @Router /api/game/preinstall [get]
func (h *PreinstallHandler) UsePreinstall(ctx *gin.Context) {
	h.lock.Lock()
	defer h.lock.Unlock()

	clusterName := context.GetClusterName(ctx)
	if strings.TrimSpace(clusterName) == "" {
		clusterName = "MyDediServer"
	}

	name := strings.TrimSpace(ctx.DefaultQuery("name", "default"))
	if name == "" {
		var body struct {
			Name string `json:"name"`
		}
		if err := ctx.ShouldBindJSON(&body); err == nil && body.Name != "" {
			name = strings.TrimSpace(body.Name)
		}
	}
	if name == "" {
		name = "default"
	}

	log.Printf("[Preinstall] 收到切换世界模板请求: 模板=%s, 集群=%s\n", name, clusterName)

	// 1. 查找对应的预设模板目录
	targetDir := findPreinstallDir(name)
	if targetDir == "" {
		log.Printf("[Preinstall] 模板未找到: %s\n", name)
		ctx.JSON(http.StatusOK, response.Response{
			Code: 400,
			Msg:  fmt.Sprintf("模板【%s】未在服务器中配置。若要使用此模板，请将预设存档放置于 static/preinstall/%s 目录下", name, name),
			Data: nil,
		})
		return
	}

	// 2. 停止当前集群所有运行中的世界
	_ = h.gameProcess.StopAll(clusterName)
	time.Sleep(1 * time.Second)

	// 3. 创建自动备份，防止数据丢失
	h.backupService.CreateBackup(clusterName, fmt.Sprintf("preinstall_%s_%s", name, time.Now().Format("20060102150405")))

	// 4. 读取并临时保存关键凭证文件（token、黑白名单、管理员列表等）
	clusterPath := h.archive.ClusterPath(clusterName)
	fileUtils.CreateDirIfNotExists(clusterPath)

	preservedFiles := make(map[string]string)
	keysToPreserve := []string{
		"cluster_token.txt",
		"adminlist.txt",
		"blocklist.txt",
		"blacklist.txt",
		"whitelist.txt",
	}
	for _, fileName := range keysToPreserve {
		p := filepath.Join(clusterPath, fileName)
		if fileUtils.Exists(p) {
			content, err := fileUtils.ReadFile(p)
			if err == nil && content != "" {
				preservedFiles[fileName] = content
			}
		}
	}

	// 备份原 cluster.ini 避免覆盖
	var clusterIniContent string
	if fileUtils.Exists(filepath.Join(clusterPath, "cluster.ini")) {
		clusterIniContent, _ = fileUtils.ReadFile(filepath.Join(clusterPath, "cluster.ini"))
	}

	// 5. 清理现有关卡目录，避免旧世界的存档文件干扰新地图生成
	levelCfg, _ := h.levelConfigUtils.GetLevelConfig(clusterName)
	if levelCfg != nil {
		for _, item := range levelCfg.LevelList {
			if item.File != "" {
				_ = fileUtils.DeleteDir(filepath.Join(clusterPath, item.File))
			}
		}
	}
	_ = fileUtils.DeleteDir(filepath.Join(clusterPath, "Master"))
	_ = fileUtils.DeleteDir(filepath.Join(clusterPath, "Caves"))

	// 6. 将模板目录下的所有内容复制到 clusterPath
	if err := copyDirContents(targetDir, clusterPath); err != nil {
		log.Printf("[Preinstall] 复制模板文件失败: %v\n", err)
		ctx.JSON(http.StatusOK, response.Response{
			Code: 500,
			Msg:  fmt.Sprintf("应用模板失败: %v", err),
			Data: nil,
		})
		return
	}

	// 7. 还原凭证文件
	for fileName, content := range preservedFiles {
		_ = fileUtils.WriterTXT(filepath.Join(clusterPath, fileName), content)
	}
	if !fileUtils.Exists(filepath.Join(clusterPath, "cluster.ini")) && clusterIniContent != "" {
		_ = fileUtils.WriterTXT(filepath.Join(clusterPath, "cluster.ini"), clusterIniContent)
	}

	// 8. 刷新 level.json，保证面板页面能正确感知所有世界
	newLevelCfg, err := h.levelConfigUtils.GetLevelConfig(clusterName)
	if err == nil && newLevelCfg != nil {
		_ = h.levelConfigUtils.SaveLevelConfig(clusterName, newLevelCfg)
	}

	// 9. 为所有包含 modoverrides.lua 的世界自动配置 dedicated_server_mods_setup.lua
	dstCfg, err := h.dstConfig.GetDstConfig(clusterName)
	if err == nil {
		entries, err := os.ReadDir(clusterPath)
		if err == nil {
			var modContents []string
			for _, entry := range entries {
				if entry.IsDir() {
					modPath := filepath.Join(clusterPath, entry.Name(), "modoverrides.lua")
					if fileUtils.Exists(modPath) {
						modContent, _ := fileUtils.ReadFile(modPath)
						if modContent != "" {
							modContents = append(modContents, modContent)
						}
					}
				}
			}
			if len(modContents) > 0 {
				_ = dstUtils.SyncDedicatedServerModsSetup(dstCfg, modContents...)
			}
		}
	}

	log.Printf("[Preinstall] 世界模板【%s】应用成功, 集群: %s\n", name, clusterName)
	ctx.JSON(http.StatusOK, response.Response{
		Code: 200,
		Msg:  "切换世界模板成功",
		Data: nil,
	})
}

func findPreinstallDir(name string) string {
	candidates := []string{
		filepath.Join("./static/preinstall", name),
	}

	switch name {
	case "default", "standard", "森林和洞穴", "标准世界":
		candidates = append(candidates,
			filepath.Join("./static/preinstall", "森林和洞穴"),
			filepath.Join("./static/preinstall", "标准世界"),
		)
	case "afk", "挂机", "挂机服":
		candidates = append(candidates,
			filepath.Join("./static/preinstall", "挂机"),
			filepath.Join("./static/preinstall", "挂机服"),
		)
	}

	for _, cand := range candidates {
		if fileUtils.IsDir(cand) {
			return cand
		}
	}

	// Fallback for standard world if preinstall dir doesn't exist
	if name == "default" || name == "standard" || name == "森林和洞穴" || name == "标准世界" {
		if fileUtils.IsDir("./static/Master") && fileUtils.IsDir("./static/Caves") {
			return "./static/preinstall/标准世界"
		}
	}

	return ""
}

func copyDirContents(srcDir, dstDir string) error {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return err
	}
	for _, entry := range entries {
		src := filepath.Join(srcDir, entry.Name())
		dst := filepath.Join(dstDir, entry.Name())
		if entry.IsDir() {
			if err := copyDir(src, dst); err != nil {
				return err
			}
		} else {
			if err := copyFile(src, dst); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
