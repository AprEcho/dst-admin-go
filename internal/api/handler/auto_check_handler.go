package handler

import (
	"dst-admin-go/internal/model"
	"dst-admin-go/internal/pkg/context"
	"dst-admin-go/internal/pkg/response"
	"dst-admin-go/internal/service/autoCheck"
	"dst-admin-go/internal/service/levelConfig"
	"net/http"
	"strconv"
	"sync"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type AutoCheckHandler struct {
	autoCheckService *autoCheck.AutoCheckService
	levelConfigUtils *levelConfig.LevelConfigUtils
	db               *gorm.DB
	mu               sync.Mutex
}

func NewAutoCheckHandler(
	autoCheckService *autoCheck.AutoCheckService,
	levelConfigUtils *levelConfig.LevelConfigUtils,
	db *gorm.DB,
) *AutoCheckHandler {
	return &AutoCheckHandler{
		autoCheckService: autoCheckService,
		levelConfigUtils: levelConfigUtils,
		db:               db,
	}
}

func (h *AutoCheckHandler) RegisterRoute(router *gin.RouterGroup) {
	// 新版自动检测路由
	check2 := router.Group("/api/auto/check2")
	{
		check2.GET("", h.GetAutoCheckList2)
		check2.POST("", h.SaveAutoCheck2)
	}

	// 兼容旧版路由及辅助端点
	check := router.Group("/api/auto/check")
	{
		check.GET("", h.GetAutoCheck)
		check.POST("", h.SaveAutoCheck)
		check.GET("/status", h.GetAutoCheckStatus)
		check.GET("/master", h.EnableMasterRun)
		check.GET("/caves", h.EnableCavesRun)
		check.GET("/version", h.EnableUpdateVersion)
		check.GET("/master/mod", h.EnableMasterMod)
		check.GET("/caves/mod", h.EnableCavesMod)
	}
}

// GetAutoCheckList2 获取自动维护任务列表
func (h *AutoCheckHandler) GetAutoCheckList2(ctx *gin.Context) {
	checkType := ctx.Query("checkType")
	clusterName := context.GetClusterName(ctx)
	if clusterName == "" {
		clusterName = "MyDediServer"
	}

	// 获取世界关卡配置
	var levelList []levelConfig.Item
	config, err := h.levelConfigUtils.GetLevelConfig(clusterName)
	if err == nil && config != nil && len(config.LevelList) > 0 {
		levelList = config.LevelList
	} else {
		levelList = []levelConfig.Item{
			{Name: "地面", File: "Master"},
			{Name: "洞穴", File: "Caves"},
		}
	}

	// 查询 DB 中该集群已保存的任务
	var dbList []model.AutoCheck
	h.db.Where("cluster_name = ?", clusterName).Find(&dbList)
	dbMap := make(map[string]model.AutoCheck)
	for _, item := range dbList {
		key := item.CheckType + "_" + item.Uuid
		dbMap[key] = item
	}

	var result []model.AutoCheck

	if checkType == "" || checkType == autoCheck.CheckTypeLevelDown {
		for _, lvl := range levelList {
			key := autoCheck.CheckTypeLevelDown + "_" + lvl.File
			if item, exists := dbMap[key]; exists {
				if item.LevelName == "" {
					item.LevelName = lvl.Name
				}
				result = append(result, item)
			} else {
				result = append(result, model.AutoCheck{
					ClusterName:  clusterName,
					LevelName:    lvl.Name,
					Uuid:         lvl.File,
					Enable:       0,
					Announcement: "",
					Times:        1,
					Sleep:        5,
					Interval:     5,
					CheckType:    autoCheck.CheckTypeLevelDown,
				})
			}
		}
	}

	if checkType == "" || checkType == autoCheck.CheckTypeLevelMod {
		for _, lvl := range levelList {
			key := autoCheck.CheckTypeLevelMod + "_" + lvl.File
			if item, exists := dbMap[key]; exists {
				if item.LevelName == "" {
					item.LevelName = lvl.Name
				}
				result = append(result, item)
			} else {
				result = append(result, model.AutoCheck{
					ClusterName:  clusterName,
					LevelName:    lvl.Name,
					Uuid:         lvl.File,
					Enable:       0,
					Announcement: "",
					Times:        1,
					Sleep:        5,
					Interval:     10,
					CheckType:    autoCheck.CheckTypeLevelMod,
				})
			}
		}
	}

	if checkType == "" || checkType == autoCheck.CheckTypeUpdateGame {
		key := autoCheck.CheckTypeUpdateGame + "_"
		if item, exists := dbMap[key]; exists {
			if item.LevelName == "" {
				item.LevelName = clusterName
			}
			result = append(result, item)
		} else {
			result = append(result, model.AutoCheck{
				ClusterName:  clusterName,
				LevelName:    clusterName,
				Uuid:         "",
				Enable:       0,
				Announcement: "",
				Times:        1,
				Sleep:        5,
				Interval:     20,
				CheckType:    autoCheck.CheckTypeUpdateGame,
			})
		}
	}

	ctx.JSON(http.StatusOK, response.Response{
		Code: 200,
		Msg:  "success",
		Data: result,
	})
}

// SaveAutoCheck2 保存/更新自动维护任务配置
func (h *AutoCheckHandler) SaveAutoCheck2(ctx *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()

	clusterName := context.GetClusterName(ctx)
	if clusterName == "" {
		clusterName = "MyDediServer"
	}

	var autoCheckItem model.AutoCheck
	if err := ctx.ShouldBindJSON(&autoCheckItem); err != nil {
		ctx.JSON(http.StatusOK, response.Response{
			Code: 400,
			Msg:  "参数错误: " + err.Error(),
			Data: nil,
		})
		return
	}

	if autoCheckItem.ClusterName == "" {
		autoCheckItem.ClusterName = clusterName
	}

	// 查找是否已有记录
	var existing model.AutoCheck
	if autoCheckItem.ID > 0 {
		h.db.Where("id = ?", autoCheckItem.ID).First(&existing)
	}
	if existing.ID == 0 {
		h.db.Where("cluster_name = ? AND check_type = ? AND uuid = ?",
			autoCheckItem.ClusterName, autoCheckItem.CheckType, autoCheckItem.Uuid).First(&existing)
	}

	if existing.ID > 0 {
		autoCheckItem.ID = existing.ID
		if err := h.db.Save(&autoCheckItem).Error; err != nil {
			ctx.JSON(http.StatusOK, response.Response{
				Code: 500,
				Msg:  "保存失败: " + err.Error(),
				Data: nil,
			})
			return
		}
	} else {
		if err := h.db.Create(&autoCheckItem).Error; err != nil {
			ctx.JSON(http.StatusOK, response.Response{
				Code: 500,
				Msg:  "保存失败: " + err.Error(),
				Data: nil,
			})
			return
		}
	}

	// 同步调度任务
	if h.autoCheckService != nil {
		h.autoCheckService.UpdateTask(autoCheckItem)
	}

	ctx.JSON(http.StatusOK, response.Response{
		Code: 200,
		Msg:  "success",
		Data: autoCheckItem,
	})
}

// SaveAutoCheck 兼容老接口 POST /api/auto/check
func (h *AutoCheckHandler) SaveAutoCheck(ctx *gin.Context) {
	h.SaveAutoCheck2(ctx)
}

// GetAutoCheck 兼容老接口 GET /api/auto/check?name=...
func (h *AutoCheckHandler) GetAutoCheck(ctx *gin.Context) {
	name := ctx.Query("name")
	var autoCheckItem model.AutoCheck
	h.db.Where("name = ?", name).First(&autoCheckItem)
	ctx.JSON(http.StatusOK, response.Response{
		Code: 200,
		Msg:  "success",
		Data: autoCheckItem,
	})
}

// GetAutoCheckStatus 兼容老接口 GET /api/auto/check/status
func (h *AutoCheckHandler) GetAutoCheckStatus(ctx *gin.Context) {
	clusterName := context.GetClusterName(ctx)
	if clusterName == "" {
		clusterName = "MyDediServer"
	}
	var list []model.AutoCheck
	h.db.Where("cluster_name = ?", clusterName).Find(&list)
	res := make(map[string]int)
	for _, item := range list {
		if item.Name != "" {
			res[item.Name] = item.Enable
		}
	}
	ctx.JSON(http.StatusOK, response.Response{
		Code: 200,
		Msg:  "success",
		Data: res,
	})
}

func (h *AutoCheckHandler) setQuickEnable(ctx *gin.Context, checkType, uuid string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	clusterName := context.GetClusterName(ctx)
	if clusterName == "" {
		clusterName = "MyDediServer"
	}
	enable, _ := strconv.Atoi(ctx.DefaultQuery("enable", "0"))

	var item model.AutoCheck
	h.db.Where("cluster_name = ? AND check_type = ? AND uuid = ?", clusterName, checkType, uuid).First(&item)
	item.ClusterName = clusterName
	item.CheckType = checkType
	item.Uuid = uuid
	item.Enable = enable
	if item.Interval <= 0 {
		item.Interval = 5
	}
	h.db.Save(&item)

	if h.autoCheckService != nil {
		h.autoCheckService.UpdateTask(item)
	}

	ctx.JSON(http.StatusOK, response.Response{
		Code: 200,
		Msg:  "success",
		Data: nil,
	})
}

func (h *AutoCheckHandler) EnableMasterRun(ctx *gin.Context) {
	h.setQuickEnable(ctx, autoCheck.CheckTypeLevelDown, "Master")
}

func (h *AutoCheckHandler) EnableCavesRun(ctx *gin.Context) {
	h.setQuickEnable(ctx, autoCheck.CheckTypeLevelDown, "Caves")
}

func (h *AutoCheckHandler) EnableUpdateVersion(ctx *gin.Context) {
	h.setQuickEnable(ctx, autoCheck.CheckTypeUpdateGame, "")
}

func (h *AutoCheckHandler) EnableMasterMod(ctx *gin.Context) {
	h.setQuickEnable(ctx, autoCheck.CheckTypeLevelMod, "Master")
}

func (h *AutoCheckHandler) EnableCavesMod(ctx *gin.Context) {
	h.setQuickEnable(ctx, autoCheck.CheckTypeLevelMod, "Caves")
}
