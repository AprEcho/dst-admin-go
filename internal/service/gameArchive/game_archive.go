package gameArchive

import (
	"dst-admin-go/internal/config"
	"dst-admin-go/internal/pkg/utils/dstUtils"
	"dst-admin-go/internal/pkg/utils/fileUtils"
	"dst-admin-go/internal/pkg/utils/luaUtils"
	"dst-admin-go/internal/service/archive"
	"dst-admin-go/internal/service/gameConfig"
	"dst-admin-go/internal/service/level"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type GameArchive struct {
	gameConfig *gameConfig.GameConfig
	level      *level.LevelService
	archive    *archive.PathResolver
}

func NewGameArchive(gameConfig *gameConfig.GameConfig, level *level.LevelService, archive *archive.PathResolver) *GameArchive {
	return &GameArchive{
		gameConfig: gameConfig,
		level:      level,
		archive:    archive,
	}
}

type GameArchiveInfo struct {
	ClusterName        string `json:"clusterName"`
	ClusterDescription string `json:"clusterDescription"`
	ClusterPassword    string `json:"clusterPassword"`
	GameMod            string `json:"gameMod"`
	MaxPlayers         int    `json:"maxPlayers"`
	Mods               int    `json:"mods"`
	IpConnect          string `json:"ipConnect"`
	Port               uint   `json:"port"`
	Ip                 string `json:"ip"`
	Meta               Meta   `json:"meta"`
	Version            int64  `json:"version"`
	LastVersion        int64  `json:"lastVersion"`
}

type Clock struct {
	TotalTimeInPhase     int     `lua:"totaltimeinphase"`
	Cycles               int     `lua:"cycles"`
	Phase                string  `lua:"phase"`
	RemainingTimeInPhase float64 `lua:"remainingtimeinphase"`
	MooomPhaseCycle      int     `lua:"mooomphasecycle"`
	Segs                 Segs    `lua:"segs"`
}

type Segs struct {
	Night int `lua:"night"`
	Day   int `lua:"day"`
	Dusk  int `lua:"dusk"`
}

type IsRandom struct {
	Summer bool `lua:"summer"`
	Autumn bool `lua:"autumn"`
	Spring bool `lua:"spring"`
	Winter bool `lua:"winter"`
}

type Lengths struct {
	Summer int `lua:"summer"`
	Autumn int `lua:"autumn"`
	Spring int `lua:"spring"`
	Winter int `lua:"winter"`
}

type Seasons struct {
	Premode               bool                   `lua:"premode"`
	Season                string                 `lua:"season"`
	ElapsedDaysInSeason   int                    `lua:"elapseddaysinseason"`
	IsRandom              IsRandom               `lua:"israndom"`
	Lengths               Lengths                `lua:"lengths"`
	RemainingDaysInSeason int                    `lua:"remainingdaysinseason"`
	Mode                  string                 `lua:"mode"`
	TotalDaysInSeason     int                    `lua:"totaldaysinseason"`
	Segs                  map[string]interface{} `lua:"segs"`
}

type Meta struct {
	Clock   Clock   `lua:"clock"`
	Seasons Seasons `lua:"seasons"`
}

func (d *GameArchive) GetGameArchive(clusterName string) GameArchiveInfo {

	var wg sync.WaitGroup
	wg.Add(5)

	gameArchie := GameArchiveInfo{}
	basePath := d.archive.ClusterPath(clusterName)

	// 获取基础信息
	go func() {
		defer func() {
			wg.Done()
			if r := recover(); r != nil {
				fmt.Println("GetClusterIni panic:", r)
			}
		}()
		clusterIni, _ := d.gameConfig.GetClusterIni(clusterName)
		gameArchie.ClusterName = clusterIni.ClusterName
		gameArchie.ClusterDescription = clusterIni.ClusterDescription
		gameArchie.ClusterPassword = clusterIni.ClusterPassword
		gameArchie.GameMod = clusterIni.GameMode
		gameArchie.MaxPlayers = int(clusterIni.MaxPlayers)
	}()

	// 获取mod数量
	go func() {
		defer func() {
			wg.Done()
			if r := recover(); r != nil {
				fmt.Println("GetModCount panic:", r)
			}
		}()
		masterModPath := path.Join(basePath, "Master", "modoverrides.lua")
		if !fileUtils.Exists(masterModPath) {
			masterModPath = path.Join(basePath, "master", "modoverrides.lua")
		}
		masterModoverrides, err := fileUtils.ReadFile(masterModPath)
		if err != nil {
			gameArchie.Mods = 0
		} else {
			gameArchie.Mods = len(dstUtils.WorkshopIds(masterModoverrides))
		}
	}()

	// 获取天数和季节
	go func() {
		defer func() {
			wg.Done()
			if r := recover(); r != nil {
			}
		}()
		gameArchie.Meta = d.Snapshoot(clusterName)
	}()

	// 获取直连ip
	go func() {
		defer func() {
			wg.Done()
			if r := recover(); r != nil {

			}
		}()
		clusterIni, _ := d.gameConfig.GetClusterIni(clusterName)
		password := clusterIni.ClusterPassword
		serverIniPath := path.Join(basePath, "Master", "server.ini")
		if !fileUtils.Exists(serverIniPath) {
			serverIniPath = path.Join(basePath, "master", "server.ini")
		}
		serverIni := d.level.GetServerIni(serverIniPath, true)
		wanip := config.Cfg.WanIP
		if wanip != "" {

		} else {
			ipv4, err := d.GetPublicIP()
			if err != nil {
				wanip, _ = d.GetPrivateIP()
			} else {
				wanip = ipv4
			}
		}
		if wanip == "" {
			gameArchie.IpConnect = ""
		} else {
			// c_connect("IP address", port, "password")
			if password != "" {
				gameArchie.IpConnect = "c_connect(\"" + wanip + "\"," + strconv.Itoa(int(serverIni.ServerPort)) + ",\"" + password + "\"" + ")"
			} else {
				gameArchie.IpConnect = "c_connect(\"" + wanip + "\"," + strconv.Itoa(int(serverIni.ServerPort)) + ")"
			}
		}
		gameArchie.Port = serverIni.ServerPort
		gameArchie.Ip = wanip

	}()

	go func() {
		defer func() {
			wg.Done()
			if r := recover(); r != nil {

			}
		}()
		localVersion, _ := d.archive.GetLocalDstVersion(clusterName)
		version, _ := d.archive.GetLastDstVersion()

		gameArchie.Version = localVersion
		gameArchie.LastVersion = version
	}()

	wg.Wait()

	return gameArchie
}

var (
	cachedPublicIP   string
	cachedPublicIPAt time.Time
	publicIPMutex    sync.RWMutex
)

/*
- 以下均是公开接口，返回纯文本数据
- 增加内存缓存与并发竞速获取，避免每次页面请求串行超时卡死
- 饥荒暂不支持IPv6
*/
func (d *GameArchive) GetPublicIP() (string, error) {
	publicIPMutex.RLock()
	if cachedPublicIP != "" && time.Since(cachedPublicIPAt) < 15*time.Minute {
		defer publicIPMutex.RUnlock()
		return cachedPublicIP, nil
	}
	publicIPMutex.RUnlock()

	apis := []string{
		// == 优先返回国内ip ==
		"https://myip.ipip.net",
		"https://cdid.c-ctrip.com/model-poc2/h",
		// == 国外ip接口 ==
		"https://lobby-v2.klei.com/lobby/getIP",
		"https://api.ipify.org",
		"https://ifconfig.me/ip",
		"https://checkip.amazonaws.com",
	}

	client := &http.Client{
		Timeout: 3 * time.Second,
	}

	ch := make(chan string, len(apis))
	var ipWg sync.WaitGroup
	ipv4Regex := regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}\b`)

	for _, api := range apis {
		ipWg.Add(1)
		go func(url string) {
			defer ipWg.Done()
			resp, err := client.Get(url)
			if err != nil {
				return
			}
			body, err := ioutil.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil {
				return
			}
			text := strings.TrimSpace(string(body))
			if match := ipv4Regex.FindString(text); match != "" {
				if ip := net.ParseIP(match); ip != nil && ip.To4() != nil {
					select {
					case ch <- match:
					default:
					}
				}
			}
		}(api)
	}

	go func() {
		ipWg.Wait()
		close(ch)
	}()

	select {
	case ip := <-ch:
		if ip != "" {
			publicIPMutex.Lock()
			cachedPublicIP = ip
			cachedPublicIPAt = time.Now()
			publicIPMutex.Unlock()
			return ip, nil
		}
	case <-time.After(3 * time.Second):
	}

	return "", nil
}

func (d *GameArchive) GetPrivateIP() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}

	for _, addr := range addrs {
		var ip net.IP

		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}

		if ip == nil || ip.IsLoopback() {
			continue
		}
		if ipv4 := ip.To4(); ipv4 != nil {
			return ipv4.String(), nil
		}
	}

	return "", nil
}

func (d *GameArchive) getSubPathLevel(rootP, curPath string) int {
	relPath, err := filepath.Rel(rootP, curPath)
	if err != nil {
		// 如果计算相对路径时出错，说明 curPath 不是 rootP 的子目录
		return -1
	}
	// 计算相对路径中 ".." 的数量，即为层数
	return strings.Count(relPath, "..")
}

func (d *GameArchive) FindLatestMetaFile(rootDir string) (string, error) {
	var latestFile string
	var latestModTime time.Time
	err := filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && filepath.Ext(info.Name()) == ".meta" && d.getSubPathLevel(rootDir, path) == 2 {
			if info.ModTime().After(latestModTime) {
				latestFile = path
				latestModTime = info.ModTime()
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return latestFile, nil
}

func findLatestMetaFile(directory string) (string, error) {
	// 检查指定目录是否存在
	_, err := os.Stat(directory)
	if os.IsNotExist(err) {
		return "", fmt.Errorf("目录不存在：%s", directory)
	}

	// 获取指定目录下一级的所有子目录
	subdirs, err := ioutil.ReadDir(directory)
	if err != nil {
		return "", fmt.Errorf("读取目录失败：%s", err)
	}

	// 用于存储最新的.meta文件路径和其修改时间
	var latestMetaFile string
	var latestMetaFileTime time.Time

	for _, subdir := range subdirs {
		// 检查子目录是否是目录
		if subdir.IsDir() {
			subdirPath := filepath.Join(directory, subdir.Name())

			// 获取子目录下的所有文件
			files, err := ioutil.ReadDir(subdirPath)
			if err != nil {
				return "", fmt.Errorf("读取子目录失败：%s", err)
			}

			for _, file := range files {
				// 检查文件是否是.meta文件
				if !file.IsDir() && filepath.Ext(file.Name()) == ".meta" {
					// 获取文件的修改时间
					modifiedTime := file.ModTime()

					// 如果找到的文件的修改时间比当前最新的.meta文件的修改时间更晚，则更新最新的.meta文件路径和修改时间
					if modifiedTime.After(latestMetaFileTime) {
						latestMetaFile = filepath.Join(subdirPath, file.Name())
						latestMetaFileTime = modifiedTime
					}
				}
			}
		}
	}

	if latestMetaFile == "" {
		return "", fmt.Errorf("未找到.meta文件")
	}

	return latestMetaFile, nil
}

func (d *GameArchive) Snapshoot(clusterName string) Meta {
	base := filepath.Join(d.archive.KleiBasePath(clusterName), clusterName)
	sessionPath := filepath.Join(base, "Master", "save", "session")
	if !fileUtils.Exists(sessionPath) {
		sessionPath = filepath.Join(base, "master", "save", "session")
	}
	p, err := findLatestMetaFile(sessionPath)
	if err != nil {
		fmt.Println("查找meta文件失败", err)
		return Meta{}
	}
	content, err := fileUtils.ReadFile(p)
	if err != nil {
		fmt.Println("读取meta文件失败", err)
		return Meta{}
	}
	var data Meta
	err = luaUtils.LuaTable2Struct(content[:len(content)-1], reflect.ValueOf(&data).Elem())
	if err != nil {
		fmt.Println("解析meta文件失败", err)
		return Meta{}
	}
	return data
}
