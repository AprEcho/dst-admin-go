package autoCheck

import (
	"dst-admin-go/internal/model"
	"dst-admin-go/internal/pkg/utils/dstUtils"
	"dst-admin-go/internal/pkg/utils/fileUtils"
	"dst-admin-go/internal/service/archive"
	"dst-admin-go/internal/service/dstConfig"
	"dst-admin-go/internal/service/game"
	"dst-admin-go/internal/service/levelConfig"
	"dst-admin-go/internal/service/update"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

const (
	CheckTypeUpdateGame = "UPDATE_GAME"
	CheckTypeLevelDown  = "LEVEL_DOWN"
	CheckTypeLevelMod   = "LEVEL_MOD"
)

type WorkshopItem struct {
	Size        int64 `json:"size"`
	TimeUpdated int64 `json:"timeupdated"`
	Manifest    int64 `json:"manifest"`
}

type AutoCheckService struct {
	db               *gorm.DB
	pathResolver     *archive.PathResolver
	levelConfigUtils *levelConfig.LevelConfigUtils
	gameProcess      game.Process
	updateService    update.Update
	dstConfig        dstConfig.Config
	tasks            sync.Map // key -> chan struct{}
	mu               sync.Mutex
}

func NewAutoCheckService(
	db *gorm.DB,
	pathResolver *archive.PathResolver,
	levelConfigUtils *levelConfig.LevelConfigUtils,
	gameProcess game.Process,
	updateService update.Update,
	dstConfig dstConfig.Config,
) *AutoCheckService {
	service := &AutoCheckService{
		db:               db,
		pathResolver:     pathResolver,
		levelConfigUtils: levelConfigUtils,
		gameProcess:      gameProcess,
		updateService:    updateService,
		dstConfig:        dstConfig,
	}

	service.startEnabledTasks()

	return service
}

func (s *AutoCheckService) taskKey(clusterName, checkType, uuid string) string {
	return fmt.Sprintf("%s_%s_%s", clusterName, checkType, uuid)
}

func (s *AutoCheckService) startEnabledTasks() {
	var tasks []model.AutoCheck
	if err := s.db.Where("enable = ?", 1).Find(&tasks).Error; err != nil {
		log.Printf("[AutoCheck] 查询已启用的自动任务失败: %v\n", err)
		return
	}
	for _, task := range tasks {
		s.StartTask(task)
	}
}

func (s *AutoCheckService) StartTask(task model.AutoCheck) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := s.taskKey(task.ClusterName, task.CheckType, task.Uuid)
	if oldCh, ok := s.tasks.Load(key); ok {
		close(oldCh.(chan struct{}))
		s.tasks.Delete(key)
	}

	if task.Enable != 1 {
		return
	}

	stopCh := make(chan struct{})
	s.tasks.Store(key, stopCh)

	go func(t model.AutoCheck, ch chan struct{}) {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[AutoCheck] 任务 goroutine 异常: %v\n", r)
			}
		}()

		intervalMin := t.Interval
		if intervalMin <= 0 {
			intervalMin = 5
		}
		interval := time.Duration(intervalMin) * time.Minute

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ch:
				return
			case <-ticker.C:
				s.executeCheck(t)
			}
		}
	}(task, stopCh)
}

func (s *AutoCheckService) StopTask(clusterName, checkType, uuid string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := s.taskKey(clusterName, checkType, uuid)
	if ch, ok := s.tasks.Load(key); ok {
		close(ch.(chan struct{}))
		s.tasks.Delete(key)
	}
}

func (s *AutoCheckService) UpdateTask(task model.AutoCheck) {
	if task.Enable == 1 {
		s.StartTask(task)
	} else {
		s.StopTask(task.ClusterName, task.CheckType, task.Uuid)
	}
}

func (s *AutoCheckService) StopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.tasks.Range(func(key, value any) bool {
		if ch, ok := value.(chan struct{}); ok {
			close(ch)
		}
		s.tasks.Delete(key)
		return true
	})
}

func (s *AutoCheckService) executeCheck(task model.AutoCheck) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[AutoCheck] 执行检测异常: %v\n", r)
		}
	}()

	switch task.CheckType {
	case CheckTypeLevelDown:
		isRunning, err := s.gameProcess.Status(task.ClusterName, task.Uuid)
		if err == nil && !isRunning {
			log.Printf("[AutoCheck] 检测到世界 [%s/%s] 未在运行，15秒后进行复检...", task.ClusterName, task.Uuid)
			time.Sleep(15 * time.Second)
			isRunning2, _ := s.gameProcess.Status(task.ClusterName, task.Uuid)
			if !isRunning2 {
				log.Printf("[AutoCheck] 世界 [%s/%s] 依然处于停止状态，正在自动拉起...", task.ClusterName, task.Uuid)
				if task.Announcement != "" {
					s.sendAnnouncement(task.ClusterName, task.Uuid, task.Announcement, task.Sleep, task.Times)
				}
				if err := s.gameProcess.Start(task.ClusterName, task.Uuid); err != nil {
					log.Printf("[AutoCheck] 自动拉起世界 [%s/%s] 失败: %v\n", task.ClusterName, task.Uuid, err)
				}
			}
		}

	case CheckTypeUpdateGame:
		lastVer, err1 := s.pathResolver.GetLastDstVersion()
		localVer, err2 := s.pathResolver.GetLocalDstVersion(task.ClusterName)
		if err1 == nil && err2 == nil && lastVer > localVer && localVer > 0 {
			log.Printf("[AutoCheck] 发现游戏新版本 (本地: %d, 远程最新: %d)，执行自动更新...", localVer, lastVer)
			if task.Announcement != "" {
				s.sendAnnouncement(task.ClusterName, "", task.Announcement, task.Sleep, task.Times)
			}
			if err := s.updateService.Update(task.ClusterName); err != nil {
				log.Printf("[AutoCheck] 自动更新游戏失败: %v\n", err)
			}
		}

	case CheckTypeLevelMod:
		if s.hasModUpdate(task.ClusterName, task.Uuid) {
			log.Printf("[AutoCheck] 检测到世界 [%s/%s] 模组有更新，正在重启应用更新...", task.ClusterName, task.Uuid)
			if task.Announcement != "" {
				s.sendAnnouncement(task.ClusterName, task.Uuid, task.Announcement, task.Sleep, task.Times)
			}
			s.gameProcess.Stop(task.ClusterName, task.Uuid)
			time.Sleep(10 * time.Second)
			s.gameProcess.Start(task.ClusterName, task.Uuid)
		}
	}
}

func (s *AutoCheckService) sendAnnouncement(clusterName, levelName, announcement string, sleep, times int) {
	if strings.TrimSpace(announcement) == "" {
		return
	}
	if times <= 0 {
		times = 1
	}
	if sleep <= 0 {
		sleep = 3
	}

	lines := strings.Split(announcement, "\n")
	for i := 0; i < times; i++ {
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			cmd := fmt.Sprintf("c_announce(\"%s\")", line)
			if levelName != "" {
				s.gameProcess.Command(clusterName, levelName, cmd)
			} else {
				if config, err := s.levelConfigUtils.GetLevelConfig(clusterName); err == nil {
					for _, lvl := range config.LevelList {
						s.gameProcess.Command(clusterName, lvl.File, cmd)
					}
				}
			}
			time.Sleep(300 * time.Millisecond)
		}
		if i < times-1 {
			time.Sleep(time.Duration(sleep) * time.Second)
		}
	}
}

func (s *AutoCheckService) hasModUpdate(clusterName, levelName string) bool {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[AutoCheck] 检测模组更新异常: %v\n", r)
		}
	}()

	modoverridesPath := s.pathResolver.ModoverridesPath(clusterName, levelName)
	if !fileUtils.Exists(modoverridesPath) {
		return false
	}
	content, err := fileUtils.ReadFile(modoverridesPath)
	if err != nil {
		return false
	}
	workshopIds := dstUtils.WorkshopIds(content)
	if len(workshopIds) == 0 {
		return false
	}

	acfPath := s.pathResolver.GetUgcAcfPath(clusterName, levelName)
	if !fileUtils.Exists(acfPath) {
		return false
	}
	acfWorkshops := parseACFFile(acfPath)
	if len(acfWorkshops) == 0 {
		return false
	}

	activeModMap := make(map[string]WorkshopItem)
	for _, id := range workshopIds {
		if item, ok := acfWorkshops[id]; ok {
			activeModMap[id] = item
		}
	}
	if len(activeModMap) == 0 {
		return false
	}

	return diffSteamModInfo(activeModMap)
}

func parseACFFile(filePath string) map[string]WorkshopItem {
	lines, err := fileUtils.ReadLnFile(filePath)
	if err != nil {
		return nil
	}
	parsingWorkshopItemsInstalled := false
	workshopItems := make(map[string]WorkshopItem)
	var currentItemID string
	var currentItem WorkshopItem
	for _, line := range lines {
		if strings.Contains(line, "WorkshopItemsInstalled") {
			parsingWorkshopItemsInstalled = true
			continue
		}
		if strings.Contains(line, "{") && parsingWorkshopItemsInstalled {
			continue
		}
		if strings.Contains(line, "}") && parsingWorkshopItemsInstalled {
			continue
		}

		if parsingWorkshopItemsInstalled {
			replace := strings.ReplaceAll(line, "\t", "")
			replace = strings.ReplaceAll(replace, "\"", "")
			replace = strings.TrimSpace(replace)
			if _, err := strconv.Atoi(replace); err == nil {
				if currentItemID != "" {
					workshopItems[currentItemID] = currentItem
				}
				currentItemID = replace
				currentItem = WorkshopItem{}
			} else if strings.Contains(line, "size") {
				parts := strings.Fields(line)
				if len(parts) >= 2 {
					sizeStr := strings.Trim(parts[len(parts)-1], "\"")
					if size, err := strconv.ParseInt(sizeStr, 10, 64); err == nil {
						currentItem.Size = size
					}
				}
			} else if strings.Contains(line, "timeupdated") {
				parts := strings.Fields(line)
				if len(parts) >= 2 {
					timeStr := strings.Trim(parts[len(parts)-1], "\"")
					if timeUpdated, err := strconv.ParseInt(timeStr, 10, 64); err == nil {
						currentItem.TimeUpdated = timeUpdated
					}
				}
			}
		}
	}
	if currentItemID != "" {
		workshopItems[currentItemID] = currentItem
	}
	return workshopItems
}

func diffSteamModInfo(activeModMap map[string]WorkshopItem) bool {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[AutoCheck] 请求Steam模组更新异常: %v\n", r)
		}
	}()

	var modIds []string
	for key := range activeModMap {
		modIds = append(modIds, key)
	}
	if len(modIds) == 0 {
		return false
	}

	steamAPIKey := "73DF9F781D195DFD3D19DED1CB72EEE6"
	urlStr := "http://api.steampowered.com/IPublishedFileService/GetDetails/v1/"
	data := url.Values{}
	data.Set("key", steamAPIKey)
	data.Set("language", "6")
	for i, id := range modIds {
		data.Set(fmt.Sprintf("publishedfileids[%d]", i), id)
	}
	urlStr = urlStr + "?" + data.Encode()

	client := &http.Client{Timeout: 8 * time.Second}
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false
	}

	respMap, ok := result["response"].(map[string]interface{})
	if !ok {
		return false
	}
	dataList, ok := respMap["publishedfiledetails"].([]interface{})
	if !ok {
		return false
	}

	for _, item := range dataList {
		modMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if timeUpdatedVal, exists := modMap["time_updated"]; exists {
			if timeUpdatedFloat, ok := timeUpdatedVal.(float64); ok {
				modId, _ := modMap["publishedfileid"].(string)
				if localItem, found := activeModMap[modId]; found {
					if int64(timeUpdatedFloat) > localItem.TimeUpdated && localItem.TimeUpdated > 0 {
						return true
					}
				}
			}
		}
	}

	return false
}
