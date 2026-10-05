package api

import (
	"dst-admin-go/internal/api/handler"
	"dst-admin-go/internal/collect"
	"dst-admin-go/internal/config"
	"dst-admin-go/internal/middleware"
	"dst-admin-go/internal/service/archive"
	"dst-admin-go/internal/service/backup"
	"dst-admin-go/internal/service/dstConfig"
	"dst-admin-go/internal/service/dstMap"
	"dst-admin-go/internal/service/game"
	"dst-admin-go/internal/service/gameArchive"
	"dst-admin-go/internal/service/gameConfig"
	"dst-admin-go/internal/service/level"
	"dst-admin-go/internal/service/levelConfig"
	"dst-admin-go/internal/service/login"
	"dst-admin-go/internal/service/mod"
	"dst-admin-go/internal/service/player"
	"dst-admin-go/internal/service/schedule"
	"dst-admin-go/internal/service/update"
	"log"
	"strings"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/memstore"
	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	"github.com/swaggo/gin-swagger"

	"gorm.io/gorm"
)

func NewRoute(cfg *config.Config, db *gorm.DB) *gin.Engine {
	app := gin.Default()
	store := memstore.NewStore([]byte("secret"))
	store.Options(sessions.Options{
		Path:     "/",
		MaxAge:   int(60 * 24 * 7 * time.Minute.Seconds()),
		HttpOnly: true,
	})
	app.Use(sessions.Sessions("token", store))
	app.Use(middleware.Recover)

	app.GET("/hello", func(ctx *gin.Context) {
		ctx.String(200, "Hello! Dont starve together")
	})
	// Swagger UI
	app.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	RegisterStaticFile(app)
	Register(cfg, db, app.Group(""))
	return app
}

func initCollectors(archive *archive.PathResolver, dstConfigService dstConfig.Config) {
	clusterName := "MyDediServer"
	getDstConfig, err := dstConfigService.GetDstConfig(clusterName)
	if err == nil && strings.TrimSpace(getDstConfig.Cluster) != "" {
		clusterName = strings.TrimSpace(getDstConfig.Cluster)
	}
	clusterPath := archive.ClusterPath(clusterName)
	log.Printf("[Collector] 初始化日志监听器, 集群: %s, 路径: %s\n", clusterName, clusterPath)
	newCollect := collect.NewCollect(clusterPath, clusterName)
	collect.Collector = newCollect
	collect.Collector.StartCollect()
}

func RegisterStaticFile(app *gin.Engine) {

	defer func() {
		if r := recover(); r != nil {
			log.Printf("[Static] 注册静态文件异常: %v\n", r)
		}
	}()

	staticCache := func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=31536000")
		c.Next()
	}

	assetsGroup := app.Group("/assets", staticCache)
	assetsGroup.Static("", "./dist/assets")

	miscGroup := app.Group("/misc", staticCache)
	miscGroup.Static("", "./dist/misc")

	staticJsGroup := app.Group("/static/js", staticCache)
	staticJsGroup.Static("", "./dist/static/js")

	staticCssGroup := app.Group("/static/css", staticCache)
	staticCssGroup.Static("", "./dist/static/css")

	staticImgGroup := app.Group("/static/img", staticCache)
	staticImgGroup.Static("", "./dist/static/img")

	staticFontsGroup := app.Group("/static/fonts", staticCache)
	staticFontsGroup.Static("", "./dist/static/fonts")

	staticMediaGroup := app.Group("/static/media", staticCache)
	staticMediaGroup.Static("", "./dist/static/media")

	app.StaticFile("/favicon.ico", "./dist/favicon.ico")
	app.StaticFile("/asset-manifest.json", "./dist/asset-manifest.json")

	// 首页入口文件 index.html 绝不能长久缓存，必须每次重新验证
	app.GET("/", func(c *gin.Context) {
		c.Header("Cache-Control", "no-cache, no-store, must-revalidate")
		c.Header("Pragma", "no-cache")
		c.Header("Expires", "0")
		c.File("./dist/index.html")
	})
}

func Register(cfg *config.Config, db *gorm.DB, router *gin.RouterGroup) {

	// service
	dstConfigService := dstConfig.NewDstConfig(db)
	updateService := update.NewUpdateService(dstConfigService)
	resolverService, _ := archive.NewPathResolver(dstConfigService)
	loginService := login.NewLoginService(cfg)
	levelConfigUtils := levelConfig.NewLevelConfigUtils(resolverService)
	gameProcess := game.NewGame(dstConfigService, levelConfigUtils)

	gameConfigService := gameConfig.NewGameConfig(resolverService, levelConfigUtils)
	backupService := backup.NewBackupService(resolverService, dstConfigService, gameProcess)
	levelService := level.NewLevelService(gameProcess, dstConfigService, resolverService, levelConfigUtils)
	playerService := player.NewPlayerService(resolverService)
	gameArchiveService := gameArchive.NewGameArchive(gameConfigService, levelService, resolverService)
	modService := mod.NewModService(db, dstConfigService, resolverService)
	scheduleService := schedule.NewScheduleService(db, gameProcess, backupService, updateService, dstConfigService)

	dstMapGenerator := dstMap.NewDSTMapGenerator()

	// init
	initCollectors(resolverService, dstConfigService)

	//  handler
	updateHandler := handler.NewUpdateHandler(updateService)
	gameHandler := handler.NewGameHandler(gameProcess, levelService, gameArchiveService, levelConfigUtils, resolverService)
	gameConfigHandler := handler.NewGameConfigHandler(gameConfigService)
	dstConfigHandler := handler.NewDstConfigHandler(dstConfigService, resolverService)
	loginHandler := handler.NewLoginHandler(loginService)
	backupHandler := handler.NewBackupHandler(backupService)
	levelHandler := handler.NewLevelHandler(levelService)
	playerHandler := handler.NewPlayerHandler(playerService, gameProcess)
	levelLogHandler := handler.NewLevelLogHandler(resolverService)
	kvHandler := handler.NewKvHandler(db)
	dstApiHandler := handler.NewDstApiHandler()
	dstMapHandler := handler.NewDstMapHandler(resolverService, dstMapGenerator)
	playerLogHandler := handler.NewPlayerLogHandler()
	statisticsHandler := handler.NewStatisticsHandler()
	modHandler := handler.NewModHandler(modService, dstConfigService)
	scheduleHandler := handler.NewScheduleHandler(scheduleService)
	preinstallHandler := handler.NewPreinstallHandler(gameProcess, backupService, resolverService, dstConfigService, levelConfigUtils, levelService)

	// 中间件
	// 针对所有 API 路由强制设置无缓存响应头，杜绝浏览器对 GET 请求产生任何 disk/memory cache
	router.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "no-cache, no-store, must-revalidate")
		c.Header("Pragma", "no-cache")
		c.Header("Expires", "0")
		c.Next()
	})
	router.Use(middleware.Authentication(loginService))
	router.Use(middleware.ClusterMiddleware(dstConfigService))

	//  route
	updateHandler.RegisterRoute(router)
	gameHandler.RegisterRoute(router)
	gameConfigHandler.RegisterRoute(router)
	dstConfigHandler.RegisterRoute(router)
	loginHandler.RegisterRoute(router)
	backupHandler.RegisterRoute(router)
	levelHandler.RegisterRoute(router)
	playerHandler.RegisterRoute(router)
	levelLogHandler.RegisterRoute(router)
	kvHandler.RegisterRoute(router)
	dstApiHandler.RegisterRoute(router)
	dstMapHandler.RegisterRoute(router)
	playerLogHandler.RegisterRoute(router)
	statisticsHandler.RegisterRoute(router)
	modHandler.RegisterRoute(router)
	scheduleHandler.RegisterRoute(router)
	preinstallHandler.RegisterRoute(router)

}
