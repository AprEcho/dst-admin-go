package handler

import (
	"bufio"
	"bytes"
	"context"
	clusterContext "dst-admin-go/internal/pkg/context"
	"dst-admin-go/internal/pkg/response"
	"dst-admin-go/internal/pkg/utils/fileUtils"
	"dst-admin-go/internal/service/archive"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type LevelLogHandler struct {
	archive *archive.PathResolver
}

func NewLevelLogHandler(archive *archive.PathResolver) *LevelLogHandler {
	return &LevelLogHandler{
		archive: archive,
	}
}
func (h *LevelLogHandler) RegisterRoute(router *gin.RouterGroup) {
	router.GET("/api/game/log/stream", h.Stream)
	router.GET("/api/game/level/server/log", h.GetServerLog)
	router.GET("/api/game/level/server/download", h.DownloadServerLog)
}

// Stream 服务器日志流
// @Summary 服务器日志流
// @Description 获取指定世界的实时日志流 (SSE)
// @Tags log
// @Accept text/event-stream
// @Produce text/event-stream
// @Param clusterName query string false "集群名称"
// @Param levelName query string true "世界名称"
// @Success 200 {string} string "SSE 格式的日志流"
// @Router /api/game/log/stream [get]
func (h *LevelLogHandler) Stream(c *gin.Context) {
	clusterName := clusterContext.GetClusterName(c)
	levelName := c.Query("levelName")
	if clusterName == "" || levelName == "" {
		c.JSON(400, gin.H{"error": "cluster and level required"})
		return
	}

	w := c.Writer
	flusher, ok := w.(http.Flusher)
	if !ok {
		c.JSON(500, gin.H{"error": "streaming unsupported"})
		return
	}

	// SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // nginx

	ctx := c.Request.Context()

	// 1️⃣ snapshot：将全部 snapshot 行合并为一条或少量事件发送，避免前端 Monaco 每次收到单行都执行完整 AST 解析导致浏览器主线程卡死
	serverLogPath := h.archive.ServerLogPath(clusterName, levelName)
	lines, err := reader.Snapshot(serverLogPath, 100)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	if len(lines) > 0 {
		writeSSE(w, "log", strings.Join(lines, "\n"))
		flusher.Flush()
	}

	// 2️⃣ follow：微批处理，遇到连续高频日志时合并发送，极大降低前端渲染压力
	ch, cancel, err := reader.Follow(serverLogPath)
	if err != nil {
		writeSSE(w, "error", err.Error())
		flusher.Flush()
		return
	}
	defer cancel()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case line, ok := <-ch:
			if !ok {
				return
			}
			batch := []string{line}
			drain := true
			for drain && len(batch) < 50 {
				select {
				case nextLine, nextOk := <-ch:
					if !nextOk {
						drain = false
					} else {
						batch = append(batch, nextLine)
					}
				default:
					drain = false
				}
			}
			writeSSE(w, "log", strings.Join(batch, "\n"))
			flusher.Flush()

		case <-heartbeat.C:
			writeSSE(w, "ping", "")
			flusher.Flush()
		}
	}
}

// GetServerLog 获取服务器日志 swagger 注释
// @Summary 获取服务器日志
// @Description 获取指定世界的服务器日志（默认最近100行）
// @Tags log
// @Produce application/json
// @Param clusterName query string false "集群名称"
// @Param levelName query string true "世界名称"
// @Param lines query string false "返回日志行数，默认为100"
// @Success 200 {object} response.Response{data=[]string} "服务器日志列表"
// @Router /api/game/level/server/log [get]
func (h *LevelLogHandler) GetServerLog(ctx *gin.Context) {
	clusterName := clusterContext.GetClusterName(ctx)
	levelName := ctx.Query("levelName")
	lines := ctx.DefaultQuery("lines", "100")
	if clusterName == "" || levelName == "" {
		ctx.JSON(400, gin.H{"error": "cluster and level required"})
		return
	}
	serverLogPath := h.archive.ServerLogPath(clusterName, levelName)
	linesInt, err := strconv.Atoi(lines)
	if err != nil {
		ctx.JSON(400, gin.H{"error": "lines must be a number"})
		return
	}
	read, err := fileUtils.ReverseRead(serverLogPath, uint(linesInt))
	if err != nil {
		if os.IsNotExist(err) {
			ctx.JSON(200, response.Response{
				Code: 200,
				Data: []string{},
				Msg:  "success",
			})
			return
		}
		ctx.JSON(200, response.Response{
			Code: 500,
			Msg:  "failed to read server log: " + err.Error(),
			Data: nil,
		})
		return
	}
	ctx.JSON(200, response.Response{
		Code: 200,
		Data: read,
		Msg:  "success",
	})
}

// DownloadServerLog 下载服务器日志 swagger 注释
// @Summary 下载服务器日志
// @Description 下载指定世界的完整服务器日志文件
// @Tags log
// @Produce application/octet-stream
// @Param clusterName query string false "集群名称"
// @Param levelName query string true "世界名称"
// @Success 200 {file} file "服务器日志文件"
// @Router /api/game/level/server/download [get]
func (h *LevelLogHandler) DownloadServerLog(ctx *gin.Context) {
	clusterName := clusterContext.GetClusterName(ctx)
	levelName := ctx.Query("levelName")
	if clusterName == "" || levelName == "" {
		ctx.JSON(400, gin.H{"error": "cluster and level required"})
		return
	}
	serverLogPath := h.archive.ServerLogPath(clusterName, levelName)
	if !fileUtils.Exists(serverLogPath) {
		ctx.JSON(http.StatusNotFound, gin.H{"error": "server log file not found"})
		return
	}
	ctx.Header("Content-Type", "application/octet-stream")
	ctx.Header("Content-Disposition", "attachment; filename="+"server_log.txt")
	ctx.Header("Content-Transfer-Encoding", "binary")
	ctx.File(serverLogPath)
}

func writeSSE(w io.Writer, event, data string) {
	if event != "" {
		fmt.Fprintf(w, "event: %s\n", event)
	}

	// data 可能包含换行，必须逐行写
	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		fmt.Fprintf(w, "data: %s\n", scanner.Text())
	}

	fmt.Fprint(w, "\n")
}

var reader = NewFileLogReader()

type FileLogReader struct {
	interval time.Duration
}

func NewFileLogReader() *FileLogReader {
	return &FileLogReader{
		interval: time.Second,
	}
}

func (r *FileLogReader) Snapshot(
	serverLogPath string,
	n int,
) ([]string, error) {

	path := serverLogPath

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}

	var (
		size   = stat.Size()
		offset = size
		lines  []string
		buf    []byte
	)

	for offset > 0 && len(lines) < n {
		readSize := int64(4096)
		if offset < readSize {
			readSize = offset
		}

		offset -= readSize
		chunk := make([]byte, readSize)

		_, err := f.ReadAt(chunk, offset)
		if err != nil && err != io.EOF {
			return nil, err
		}

		buf = append(chunk, buf...)

		for {
			idx := bytes.LastIndexByte(buf, '\n')
			if idx < 0 {
				break
			}

			line := strings.TrimRight(string(buf[idx+1:]), "\r")
			lines = append(lines, line)
			buf = buf[:idx]

			if len(lines) >= n {
				break
			}
		}
	}

	// 反转
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}

	return lines, nil
}

func (r *FileLogReader) Follow(
	serverLogPath string,
) (<-chan string, func(), error) {

	path := serverLogPath

	out := make(chan string, 100)
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		defer close(out)

		var f *os.File
		var offset int64
		var reader *bufio.Reader

		defer func() {
			if f != nil {
				f.Close()
			}
		}()

		// 如果文件已存在，先打开并定位到末尾
		if initialF, err := os.Open(path); err == nil {
			if stat, err := initialF.Stat(); err == nil {
				f = initialF
				offset = stat.Size()
				reader = bufio.NewReader(f)
			} else {
				initialF.Close()
			}
		}

		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(r.interval):
				if f == nil {
					// 尝试打开文件（例如游戏刚刚启动生成了日志）
					openedF, err := os.Open(path)
					if err != nil {
						continue
					}
					f = openedF
					offset = 0
					reader = bufio.NewReader(f)
				}

				stat, err := f.Stat()
				if err != nil {
					continue
				}

				// 文件被重新创建或清空 (truncate / rotate)
				if stat.Size() < offset {
					offset = 0
					f.Seek(0, io.SeekStart)
					reader.Reset(f)
				}

				if stat.Size() == offset {
					continue
				}

				f.Seek(offset, io.SeekStart)
				reader.Reset(f)

				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						break
					}
					offset += int64(len(line))
					out <- strings.TrimRight(line, "\r\n")
				}
			}
		}
	}()

	return out, cancel, nil
}
