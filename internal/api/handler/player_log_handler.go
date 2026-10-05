package handler

import (
	"dst-admin-go/internal/database"
	"dst-admin-go/internal/model"
	"dst-admin-go/internal/pkg/response"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type PlayerLogHandler struct {
}

func NewPlayerLogHandler() *PlayerLogHandler {
	return &PlayerLogHandler{}
}

func (l *PlayerLogHandler) RegisterRoute(router *gin.RouterGroup) {
	router.GET("/api/player/log", l.PlayerLogQueryPage)

	// 删除玩家日志（支持多种路径和请求方式）
	router.POST("/api/player/log/delete", l.DeletePlayerLog)
	router.DELETE("/api/player/log/delete", l.DeletePlayerLog)
	router.DELETE("/api/player/log", l.DeletePlayerLog)
	router.DELETE("/api/player/log/:id", l.DeletePlayerLog)

	// 删除所有玩家日志（支持多种请求方式）
	router.GET("/api/player/log/delete/all", l.DeletePlayerLogAll)
	router.POST("/api/player/log/delete/all", l.DeletePlayerLogAll)
	router.DELETE("/api/player/log/delete/all", l.DeletePlayerLogAll)
	router.DELETE("/api/player/log/all", l.DeletePlayerLogAll)
}

// PlayerLogQueryPage 分页查询玩家日志
// @Summary 分页查询玩家日志
// @Description 分页查询玩家日志
// @Tags playerLog
// @Param name query string false "玩家名称"
// @Param kuId query string false "KuId"
// @Param steamId query string false "SteamId"
// @Param role query string false "角色"
// @Param action query string false "操作"
// @Param ip query string false "IP地址"
// @Param clusterName query string false "集群名称"
// @Param page query int false "页码" default(1)
// @Param size query int false "每页数量" default(10)
// @Success 200 {object} response.Response
// @Router /api/player/log [get]
func (l *PlayerLogHandler) PlayerLogQueryPage(ctx *gin.Context) {
	page, _ := strconv.Atoi(ctx.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(ctx.DefaultQuery("size", "10"))

	if page <= 0 {
		page = 1
	}
	if size <= 0 {
		size = 10
	}

	buildQuery := func() *gorm.DB {
		tx := database.Db.Model(&model.PlayerLog{})
		if name, isExist := ctx.GetQuery("name"); isExist && strings.TrimSpace(name) != "" {
			tx = tx.Where("name LIKE ?", "%"+strings.TrimSpace(name)+"%")
		}
		if kuId, isExist := ctx.GetQuery("kuId"); isExist && strings.TrimSpace(kuId) != "" {
			tx = tx.Where("ku_id LIKE ?", "%"+strings.TrimSpace(kuId)+"%")
		}
		if steamId, isExist := ctx.GetQuery("steamId"); isExist && strings.TrimSpace(steamId) != "" {
			tx = tx.Where("steam_id LIKE ? OR steamId LIKE ?", "%"+strings.TrimSpace(steamId)+"%", "%"+strings.TrimSpace(steamId)+"%")
		}
		if role, isExist := ctx.GetQuery("role"); isExist && strings.TrimSpace(role) != "" {
			tx = tx.Where("role LIKE ?", "%"+strings.TrimSpace(role)+"%")
		}
		if action, isExist := ctx.GetQuery("action"); isExist && strings.TrimSpace(action) != "" {
			tx = tx.Where("action LIKE ?", "%"+strings.TrimSpace(action)+"%")
		}
		if ip, isExist := ctx.GetQuery("ip"); isExist && strings.TrimSpace(ip) != "" {
			tx = tx.Where("ip LIKE ?", "%"+strings.TrimSpace(ip)+"%")
		}
		if clusterName, isExist := ctx.GetQuery("clusterName"); isExist && strings.TrimSpace(clusterName) != "" {
			tx = tx.Where("cluster_name = ?", strings.TrimSpace(clusterName))
		}
		return tx
	}

	var total int64
	buildQuery().Count(&total)

	playerLogs := make([]model.PlayerLog, 0)
	if err := buildQuery().Order("id desc").Limit(size).Offset((page - 1) * size).Find(&playerLogs).Error; err != nil {
		log.Println("查询玩家日志失败:", err)
	}

	totalPages := total / int64(size)
	if total%int64(size) != 0 {
		totalPages++
	}

	// 强制设置防缓存响应头，杜绝任何浏览器缓存
	ctx.Header("Cache-Control", "no-cache, no-store, must-revalidate")
	ctx.Header("Pragma", "no-cache")
	ctx.Header("Expires", "0")

	ctx.JSON(http.StatusOK, response.Response{
		Code: 200,
		Msg:  "success",
		Data: response.Page{
			Data:       playerLogs,
			Page:       page,
			Size:       size,
			Total:      total,
			TotalPages: totalPages,
		},
	})
}

// DeletePlayerLog 删除玩家日志
// @Summary 删除玩家日志
// @Description 删除指定ID的玩家日志
// @Tags playerLog
// @Param ids body []int64 true "ID列表"
// @Success 200 {object} response.Response
// @Router /api/player/log/delete [post]
func (l *PlayerLogHandler) DeletePlayerLog(ctx *gin.Context) {
	var ids []int64

	// 1. 尝试从 URL 路径参数获取: /api/player/log/:id
	if paramID := ctx.Param("id"); paramID != "" {
		if id, err := strconv.ParseInt(paramID, 10, 64); err == nil && id > 0 {
			ids = append(ids, id)
		}
	}

	// 2. 尝试从 Query 参数获取: ?id=1 或 ?ids=1,2,3
	if len(ids) == 0 {
		if qID := ctx.Query("id"); qID != "" {
			if id, err := strconv.ParseInt(qID, 10, 64); err == nil && id > 0 {
				ids = append(ids, id)
			}
		}
		if qIDs := ctx.Query("ids"); qIDs != "" {
			for _, part := range strings.Split(qIDs, ",") {
				if id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64); err == nil && id > 0 {
					ids = append(ids, id)
				}
			}
		}
		if queryArray := ctx.QueryArray("ids[]"); len(queryArray) > 0 {
			for _, part := range queryArray {
				if id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64); err == nil && id > 0 {
					ids = append(ids, id)
				}
			}
		}
	}

	// 3. 尝试从 Request Body 解析
	if len(ids) == 0 && ctx.Request.Body != nil {
		bodyBytes, err := io.ReadAll(ctx.Request.Body)
		if err == nil && len(bodyBytes) > 0 {
			// 3.1 尝试解析为原生数组: [1, 2, 3]
			var arr []int64
			if err := json.Unmarshal(bodyBytes, &arr); err == nil && len(arr) > 0 {
				ids = append(ids, arr...)
			} else {
				// 3.2 尝试解析为对象: {"ids": [1, 2, 3]} 或 {"id": 1}
				var obj struct {
					Ids []int64 `json:"ids"`
					ID  int64   `json:"id"`
				}
				if err := json.Unmarshal(bodyBytes, &obj); err == nil {
					if len(obj.Ids) > 0 {
						ids = append(ids, obj.Ids...)
					} else if obj.ID > 0 {
						ids = append(ids, obj.ID)
					}
				}
			}
		}
	}

	if len(ids) == 0 {
		response.FailWithMessage("请提供要删除的日志ID", ctx)
		return
	}

	// 彻底物理删除指定的日志
	err := database.Db.Unscoped().Where("id IN ?", ids).Delete(&model.PlayerLog{}).Error
	if err != nil {
		log.Println("删除玩家日志失败:", err)
		response.FailWithMessage("删除玩家日志失败: "+err.Error(), ctx)
		return
	}

	ctx.Header("Cache-Control", "no-cache, no-store, must-revalidate")
	ctx.Header("Pragma", "no-cache")
	ctx.Header("Expires", "0")
	response.OkWithMessage("删除成功", ctx)
}

// DeletePlayerLogAll 删除所有玩家日志
// @Summary 删除所有玩家日志
// @Description 删除所有玩家日志
// @Tags playerLog
// @Success 200 {object} response.Response
// @Router /api/player/log/delete/all [get]
func (l *PlayerLogHandler) DeletePlayerLogAll(ctx *gin.Context) {
	// 彻底物理清空所有玩家日志
	err := database.Db.Unscoped().Where("1 = 1").Delete(&model.PlayerLog{}).Error
	if err != nil {
		log.Println("清空所有玩家日志失败:", err)
		response.FailWithMessage("清空玩家日志失败: "+err.Error(), ctx)
		return
	}

	ctx.Header("Cache-Control", "no-cache, no-store, must-revalidate")
	ctx.Header("Pragma", "no-cache")
	ctx.Header("Expires", "0")
	response.OkWithMessage("清空成功", ctx)
}
