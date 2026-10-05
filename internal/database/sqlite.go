package database

import (
	"dst-admin-go/internal/config"
	"dst-admin-go/internal/model"
	"dst-admin-go/internal/pkg/utils/fileUtils"
	"log"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var Db *gorm.DB

func InitDB(config *config.Config) *gorm.DB {
	dbPath := config.GetDbPath()
	err := fileUtils.CreateFileIfNotExists(dbPath)
	if err != nil {
		log.Println(err)
	}
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		log.Println(err)
		panic("failed to connect database")
	}

	// 优化 SQLite 并发读写性能，避免 "database is locked"
	db.Exec("PRAGMA journal_mode = WAL;")
	db.Exec("PRAGMA busy_timeout = 5000;")
	db.Exec("PRAGMA synchronous = NORMAL;")

	sqlDB, err := db.DB()
	if err == nil {
		sqlDB.SetMaxOpenConns(1)
		sqlDB.SetMaxIdleConns(1)
		sqlDB.SetConnMaxLifetime(time.Hour)
	}

	Db = db
	err = db.AutoMigrate(
		&model.Spawn{},
		&model.PlayerLog{},
		&model.Connect{},
		&model.Regenerate{},
		&model.ModInfo{},
		&model.Cluster{},
		&model.JobTask{},
		&model.AutoCheck{},
		&model.Announce{},
		&model.WebLink{},
		&model.BackupSnapshot{},
		&model.LogRecord{},
		&model.KV{},
	)
	if err != nil {
		log.Println("AutoMigrate error", err)
	}
	return db
}
