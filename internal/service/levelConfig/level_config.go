package levelConfig

import (
	"dst-admin-go/internal/pkg/utils/dstUtils"
	"dst-admin-go/internal/pkg/utils/fileUtils"
	"dst-admin-go/internal/service/archive"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
)

type Item struct {
	Name       string `json:"name"`
	File       string `json:"file"`
	RunVersion int64  `json:"runVersion"`
	Version    int64  `json:"Version"`
}

type LevelConfig struct {
	LevelList []Item `json:"levelList"`
}

// LevelInfo 世界配置结构体
type LevelInfo struct {
	IsMaster          bool      `json:"isMaster"`
	LevelName         string    `json:"levelName"`
	Uuid              string    `json:"uuid"`
	RunVersion        int64     `json:"runVersion"`
	Leveldataoverride string    `json:"leveldataoverride"`
	Modoverrides      string    `json:"modoverrides"`
	ServerIni         ServerIni `json:"server_ini"`
}

type ServerIni struct {

	// [NETWORK]
	ServerPort uint `json:"server_port"`

	// [SHARD]
	IsMaster bool   `json:"is_master"`
	Name     string `json:"name"`
	Id       uint   `json:"id"`

	// [ACCOUNT]
	EncodeUserPath bool `json:"encode_user_path"`

	// [STEAM]
	AuthenticationPort uint `json:"authentication_port"`
	MasterServerPort   uint `json:"master_server_port"`
}

func NewMasterServerIni() ServerIni {
	return ServerIni{
		ServerPort:     10999,
		IsMaster:       true,
		Name:           "Master",
		Id:             10000,
		EncodeUserPath: true,
	}
}

func NewCavesServerIni() ServerIni {
	return ServerIni{
		ServerPort:         10998,
		IsMaster:           false,
		Name:               "Caves",
		Id:                 10010,
		EncodeUserPath:     true,
		AuthenticationPort: 8766,
		MasterServerPort:   27016,
	}
}

type LevelConfigUtils struct {
	archive *archive.PathResolver
}

func NewLevelConfigUtils(archive *archive.PathResolver) *LevelConfigUtils {
	return &LevelConfigUtils{
		archive: archive,
	}
}

func (p *LevelConfigUtils) initLevel(levelFolderPath string, level *LevelInfo) {

	lPath := filepath.Join(levelFolderPath, "leveldataoverride.lua")
	mPath := filepath.Join(levelFolderPath, "modoverrides.lua")
	sPath := filepath.Join(levelFolderPath, "server.ini")

	fileUtils.CreateFileIfNotExists(lPath)
	fileUtils.CreateFileIfNotExists(mPath)
	fileUtils.CreateFileIfNotExists(sPath)

	fileUtils.WriterTXT(lPath, level.Leveldataoverride)
	fileUtils.WriterTXT(mPath, level.Modoverrides)
	serverBuf := dstUtils.ParseTemplate("./static/template/server.ini", level.ServerIni)
	fileUtils.WriterTXT(sPath, serverBuf)
}

func (p *LevelConfigUtils) GetLevelConfig(clusterName string) (*LevelConfig, error) {
	clusterBasePath := p.archive.ClusterPath(clusterName)
	jsonPath := filepath.Join(clusterBasePath, "level.json")
	fileUtils.CreateDirIfNotExists(clusterBasePath)

	var config LevelConfig
	hasJson := false
	if fileUtils.Exists(jsonPath) {
		data, err := os.ReadFile(jsonPath)
		if err == nil && len(data) > 0 {
			if err := json.Unmarshal(data, &config); err == nil {
				hasJson = true
			}
		}
	}

	// 规范化已有项目的名称（将英文 "Forest" 转为 "地面"，"Caves" 转为 "洞穴"）并记录索引
	existingFiles := make(map[string]bool)
	for i := range config.LevelList {
		if config.LevelList[i].File == "Master" && (config.LevelList[i].Name == "Forest" || config.LevelList[i].Name == "") {
			config.LevelList[i].Name = "地面"
		}
		if config.LevelList[i].File == "Caves" && (config.LevelList[i].Name == "Caves" || config.LevelList[i].Name == "") {
			config.LevelList[i].Name = "洞穴"
		}
		existingFiles[config.LevelList[i].File] = true
	}

	changed := false

	// 1. 检测 Master
	masterPath := filepath.Join(clusterBasePath, "Master")
	if fileUtils.Exists(masterPath) {
		if !existingFiles["Master"] {
			config.LevelList = append([]Item{{
				Name: "地面",
				File: "Master",
			}}, config.LevelList...)
			existingFiles["Master"] = true
			changed = true
		}
	} else if len(config.LevelList) == 0 {
		// 磁盘上既没有 Master 也没有任何关卡配置时，初始化默认 Master
		masterLevelData, _ := fileUtils.ReadFile("./static/Master/leveldataoverride.lua")
		if masterLevelData == "" {
			masterLevelData = "return {}"
		}
		master := LevelInfo{
			IsMaster:          true,
			LevelName:         "地面",
			Uuid:              "Master",
			Leveldataoverride: masterLevelData,
			Modoverrides:      "return {}",
			ServerIni:         NewMasterServerIni(),
		}
		p.initLevel(masterPath, &master)
		config.LevelList = append(config.LevelList, Item{
			Name: "地面",
			File: "Master",
		})
		existingFiles["Master"] = true
		changed = true
	}

	// 2. 检测 Caves (同时兼顾大小写 Caves / caves)
	cavesDirName := ""
	if fileUtils.Exists(filepath.Join(clusterBasePath, "Caves")) {
		cavesDirName = "Caves"
	} else if fileUtils.Exists(filepath.Join(clusterBasePath, "caves")) {
		cavesDirName = "caves"
	}
	if cavesDirName != "" {
		if !existingFiles[cavesDirName] && !existingFiles["Caves"] && !existingFiles["caves"] {
			config.LevelList = append(config.LevelList, Item{
				Name: "洞穴",
				File: cavesDirName,
			})
			existingFiles[cavesDirName] = true
			changed = true
		}
	}

	// 3. 扫描 clusterBasePath 下所有包含 server.ini 的其它分片子目录（支持自定义多层世界）
	if entries, err := os.ReadDir(clusterBasePath); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				name := entry.Name()
				if strings.EqualFold(name, "Master") || strings.EqualFold(name, "Caves") || name == "save" || name == "backup" {
					continue
				}
				if fileUtils.Exists(filepath.Join(clusterBasePath, name, "server.ini")) {
					if !existingFiles[name] {
						config.LevelList = append(config.LevelList, Item{
							Name: name,
							File: name,
						})
						existingFiles[name] = true
						changed = true
					}
				}
			}
		}
	}

	// 如果 level.json 不存在或者有增补/修复，持久化回 level.json
	if !hasJson || changed {
		_ = p.SaveLevelConfig(clusterName, &config)
	}

	return &config, nil
}

func (p *LevelConfigUtils) SaveLevelConfig(clusterName string, levelConfig *LevelConfig) error {
	clusterBasePath := p.archive.ClusterPath(clusterName)
	jsonPath := filepath.Join(clusterBasePath, "level.json")
	fileUtils.CreateDirIfNotExists(clusterBasePath)

	bytes, err := json.MarshalIndent(levelConfig, "", "  ")
	if err != nil {
		log.Println("json 解析错误:", err)
		return err
	}
	return os.WriteFile(jsonPath, bytes, 0644)
}
