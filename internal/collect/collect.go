package collect

import (
	"dst-admin-go/internal/database"
	"dst-admin-go/internal/model"
	"dst-admin-go/internal/pkg/utils/fileUtils"
	"fmt"
	"log"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hpcloud/tail"
)

var (
	// 匹配日志行首时间戳: [00:01:53]:
	reLogTime = regexp.MustCompile(`^\[([^\]]+)\]:`)

	// 匹配连接 IP (兼容 Client connected from 与 New incoming connection，过滤端口):
	reClientIP = regexp.MustCompile(`(?:Client connected from|New incoming connection)\s+(?:\[LAN\]\s+)?([0-9.]+)(?:\|[0-9]+)?`)

	// 匹配 KuId 和 玩家昵称 (支持包含空格的昵称):
	reClientAuth = regexp.MustCompile(`Client authenticated:\s*\((KU_[^)]+)\)\s+(.+)`)

	// 匹配 SteamID: [Steam] Authenticated client '76561198xxxxxxxxx'
	reSteamAuth = regexp.MustCompile(`Authenticated client\s*'(\d+)'`)

	// 匹配 Session 文件路径
	reSession = regexp.MustCompile(`(?:Resuming|Serializing) user:\s*session/(.+)`)
)

var Collector *Collect

type Collect struct {
	state             chan int
	stop              chan bool
	severLogList      []string
	serverChatLogList []string
	length            int
	clusterName       string
	mu                sync.Mutex
	lastIncomingIP    string
	lastIncomingTime  time.Time
}

func NewCollect(baseLogPath string, clusterName string) *Collect {
	masterDir := "Master"
	if !fileUtils.Exists(filepath.Join(baseLogPath, "Master")) && fileUtils.Exists(filepath.Join(baseLogPath, "master")) {
		masterDir = "master"
	}
	cavesDir := "Caves"
	if !fileUtils.Exists(filepath.Join(baseLogPath, "Caves")) && fileUtils.Exists(filepath.Join(baseLogPath, "caves")) {
		cavesDir = "caves"
	}

	severLogList := []string{
		filepath.Join(baseLogPath, masterDir, "server_log.txt"),
		filepath.Join(baseLogPath, cavesDir, "server_log.txt"),
	}
	serverChatLogList := []string{
		filepath.Join(baseLogPath, masterDir, "server_chat_log.txt"),
	}
	total := len(severLogList) + len(serverChatLogList)
	collect := &Collect{
		state:             make(chan int, 1),
		severLogList:      severLogList,
		serverChatLogList: serverChatLogList,
		stop:              make(chan bool, total),
		length:            total,
		clusterName:       clusterName,
	}
	collect.state <- 1
	return collect
}

func (c *Collect) Stop() {
	close(c.stop)
}

func (c *Collect) ReCollect(baseLogPath, clusterName string) {
	for i := 0; i < c.length; i++ {
		c.stop <- true
	}
	c.severLogList = []string{
		filepath.Join(baseLogPath, "Master", "server_log.txt"),
		filepath.Join(baseLogPath, "Caves", "server_log.txt"),
	}
	c.serverChatLogList = []string{
		filepath.Join(baseLogPath, "Master", "server_chat_log.txt"),
	}
	c.length = len(c.severLogList) + len(c.serverChatLogList)
	c.stop = make(chan bool, c.length)
	c.clusterName = clusterName
	c.state <- 1
}

func (c *Collect) StartCollect() {
	go func() {
		for {
			select {
			case <-c.state:
				// 采集
				for _, s := range c.severLogList {
					go c.tailServeLog(s)
				}
				for _, s := range c.serverChatLogList {
					go c.tailServerChatLog(s)
				}
			default:
				time.Sleep(5 * time.Second)
				continue
			}
		}
	}()
}

func (c *Collect) parseSpawnRequestLog(text string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Spawn request Log text: %s\n", text)
			log.Printf("玩家角色日志解析异常: %v\n", r)
		}
	}()

	// 捕获 (1)时间, (2)动作, (3)角色, (4)玩家名
	re := regexp.MustCompile(`^\[([^\]]+)\]:\s*(.*?):\s*(\w+)\s*from\s*(.+)$`)
	matches := re.FindStringSubmatch(text)

	if len(matches) != 5 {
		// 如果日志格式不匹配，直接退出
		log.Printf("Spawn request 日志格式不匹配: %s\n", text)
		return
	}

	t := matches[1] // 00:37:41
	// action := matches[2] // Spawn request
	role := matches[3] // winona
	name := strings.TrimSpace(matches[4])

	spawn := model.Spawn{Name: name, Role: role, Time: t, ClusterName: c.clusterName}
	if err := database.Db.Create(&spawn).Error; err != nil {
		log.Printf("插入玩家 Spawn 日志失败: %v\n", err)
	}

	// 同步更新该玩家最近一条玩家日志的角色
	var lastLog model.PlayerLog
	if err := database.Db.Where("name = ? AND cluster_name = ?", name, c.clusterName).Order("created_at desc").First(&lastLog).Error; err == nil && lastLog.ID != 0 {
		if lastLog.Role == "" {
			lastLog.Role = role
			database.Db.Save(&lastLog)
		}
	}
}

func (c *Collect) parseRegenerateLog(text string) {
	defer func() {
		if err := recover(); err != nil {
			log.Println("Generating 日志解析异常:", err)
		}
	}()

	regenerate := model.Regenerate{
		ClusterName: c.clusterName,
	}
	database.Db.Create(&regenerate)
}

func (c *Collect) handleServerLogLine(text string) {
	defer func() {
		if err := recover(); err != nil {
			log.Println("server log 解析异常:", err, "line:", text)
		}
	}()

	if strings.Contains(text, "Spawn request") {
		c.parseSpawnRequestLog(text)
		return
	}
	if strings.Contains(text, "# Generating") {
		c.parseRegenerateLog(text)
		return
	}

	// 1. 匹配连接 IP (Client connected from 或 New incoming connection，过滤端口):
	if m := reClientIP.FindStringSubmatch(text); len(m) >= 2 {
		ip := m[1]
		c.mu.Lock()
		c.lastIncomingIP = ip
		c.lastIncomingTime = time.Now()
		c.mu.Unlock()
		log.Println("捕获连接 IP:", ip)
		return
	}

	// 2. 匹配 KuId 和 玩家昵称 (支持空格): Client authenticated: (KU_xxx) Name
	if m := reClientAuth.FindStringSubmatch(text); len(m) >= 3 {
		kuId := m[1]
		name := strings.TrimSpace(m[2])
		timeStr := ""
		if tMatch := reLogTime.FindStringSubmatch(text); len(tMatch) >= 2 {
			timeStr = tMatch[1]
		}

		c.mu.Lock()
		ip := ""
		if time.Since(c.lastIncomingTime) < 60*time.Second {
			ip = c.lastIncomingIP
		}
		c.mu.Unlock()

		log.Printf("捕获玩家认证: KuId=%s, Name=%s, IP=%s\n", kuId, name, ip)

		var connect model.Connect
		err := database.Db.Where("ku_id = ? AND cluster_name = ?", kuId, c.clusterName).Last(&connect).Error
		if err == nil && connect.ID != 0 {
			connect.Name = name
			if ip != "" {
				connect.Ip = ip
			}
			if timeStr != "" {
				connect.Time = timeStr
			}
			database.Db.Save(&connect)
		} else {
			connect = model.Connect{
				Ip:          ip,
				Name:        name,
				KuId:        kuId,
				Time:        timeStr,
				ClusterName: c.clusterName,
			}
			database.Db.Create(&connect)
		}

		// 同时记录到玩家日志表中，确保面板的“玩家日志”能立即展示玩家上线
		playerLog := model.PlayerLog{
			Name:        name,
			Role:        c.getSpawnRole(name).Role,
			Action:      "[JoinAnnouncement]",
			ActionDesc:  "玩家连接认证成功",
			Time:        timeStr,
			Ip:          ip,
			KuId:        kuId,
			SteamId:     connect.SteamId,
			ClusterName: c.clusterName,
		}
		if err := database.Db.Create(&playerLog).Error; err != nil {
			log.Println("插入玩家认证日志失败:", err)
		}
		return
	}

	// 3. 匹配 SteamId: Authenticated client '76561198xxxxxxxxx'
	if m := reSteamAuth.FindStringSubmatch(text); len(m) >= 2 {
		steamId := m[1]
		log.Println("捕获 SteamId:", steamId)
		var connect model.Connect
		err := database.Db.Where("cluster_name = ?", c.clusterName).Last(&connect).Error
		if err == nil && connect.ID != 0 {
			connect.SteamId = steamId
			database.Db.Save(&connect)

			// 同步更新最近一条缺失 SteamId 的玩家日志
			var lastLog model.PlayerLog
			if err := database.Db.Where("cluster_name = ? AND (steam_id = '' OR steam_id IS NULL)", c.clusterName).Order("created_at desc").First(&lastLog).Error; err == nil && lastLog.ID != 0 {
				lastLog.SteamId = steamId
				database.Db.Save(&lastLog)
			}
		}
		return
	}

	// 4. 匹配 SessionFile: Resuming/Serializing user: session/...
	if m := reSession.FindStringSubmatch(text); len(m) >= 2 {
		sessionFile := m[1]
		var connect model.Connect
		err := database.Db.Where("cluster_name = ?", c.clusterName).Last(&connect).Error
		if err == nil && connect.ID != 0 {
			connect.SessionFile = sessionFile
			database.Db.Save(&connect)
		}
		return
	}
}

func (c *Collect) tailServeLog(fileName string) {
	log.Println("开始采集 path:", fileName)
	config := tail.Config{
		ReOpen:    true,                                 // 重新打开
		Follow:    true,                                 // 是否跟随
		Location:  &tail.SeekInfo{Offset: 0, Whence: 2}, // 从文件的哪个地方开始读
		MustExist: false,                                // 文件不存在不报错
		Poll:      true,
	}
	tails, err := tail.TailFile(fileName, config)
	if err != nil {
		log.Println("文件监听失败", err)
		return
	}
	for {
		select {
		case line, ok := <-tails.Lines:
			if !ok {
				log.Println("文件读取失败", fileName)
				time.Sleep(time.Second)
			} else {
				c.handleServerLogLine(line.Text)
			}
		case <-c.stop:
			// 结束监听
			err := tails.Stop()
			if err != nil {
				log.Println("tail log 结束失败")
				return
			}
			return
		}
	}
}

func (c *Collect) parseChatLog(text string) {
	defer func() {
		if err := recover(); err != nil {
			log.Println("玩家行为日志解析异常:", err)
		}
	}()
	//[00:00:55]: [Join Announcement] 猜猜我是谁
	if strings.Contains(text, "[Join Announcement]") {
		c.parseJoin(text)
	}
	//[00:02:28]: [Leave Announcement] 猜猜我是谁
	if strings.Contains(text, "[Leave Announcement]") {
		c.parseLeave(text)
	}
	//[00:02:17]: [Death Announcement] 猜猜我是谁 死于： 采摘的红蘑菇。她变成了可怕的鬼魂！
	if strings.Contains(text, "[Death Announcement]") {
		c.parseDeath(text)
	}
	//[00:02:37]: [Resurrect Announcement] 猜猜我是谁 复活自： TMIP 控制台.
	if strings.Contains(text, "[Resurrect Announcement]") {
		c.parseResurrect(text)
	}
	//[00:03:16]: [Say] (KU_Mt-zrX8K) 猜猜我是谁: 你好啊
	if strings.Contains(text, "[Say]") {
		c.parseSay(text)
	}
	//[10:01:42]: [Announcement] 欢迎访客歪比巴卜，游玩
	if strings.Contains(text, "[Announcement]") {
		c.parseAnnouncement(text)
	}
}

func (c *Collect) parseSay(text string) {
	fmt.Println(text)

	// 正则解析日志
	re := regexp.MustCompile(`\[(.*?)\]: (\[.*?\]) \((.*?)\) (.*?): (.*)`)
	matches := re.FindStringSubmatch(text)
	if len(matches) != 6 {
		fmt.Println("无法解析日志:", text, matches)
		return
	}

	// 时间
	t := matches[1]
	// [Say]
	action := matches[2]
	kuId := matches[3]
	// 玩家名字，可包含空格
	name := matches[4]
	actionDesc := matches[5]

	// 获取玩家角色和连接信息
	spawn := c.getSpawnRole(name)
	connect := c.getConnectInfo(name)

	// 聊天日志包含 KuId，若 connects 表中未录入或缺失 KuId 则自动补录/更新
	if kuId != "" && (connect.ID == 0 || connect.KuId == "") {
		connect.KuId = kuId
		connect.Name = name
		connect.ClusterName = c.clusterName
		if connect.ID == 0 {
			database.Db.Create(connect)
		} else {
			database.Db.Save(connect)
		}
	}

	playerLog := model.PlayerLog{
		Name:        name,
		Role:        spawn.Role,
		Action:      action,
		ActionDesc:  actionDesc,
		Time:        t,
		Ip:          connect.Ip,
		KuId:        kuId,
		SteamId:     connect.SteamId,
		ClusterName: c.clusterName,
	}

	// 保存到数据库，并打印错误
	if err := database.Db.Create(&playerLog).Error; err != nil {
		fmt.Println("插入玩家日志失败:", err)
	}
}

func (c *Collect) parseResurrect(text string) {
	c.parseDeath(text)
}

func (c *Collect) parseDeath(text string) {
	fmt.Println(text)

	// 正则表达式 (1)时间, (2)动作, (3)剩余所有内容
	re := regexp.MustCompile(`^\[([^\]]+)\]:\s*(\[[^\]]+\])\s*(.*)$`)
	matches := re.FindStringSubmatch(text)
	if len(matches) != 4 {
		log.Println("无法解析 Announcement Log (正则不匹配):", text)
		return
	}

	t := matches[1]
	action := matches[2]
	// 擦屁股
	action = strings.ReplaceAll(action, " ", "")
	rest := strings.TrimSpace(matches[3]) // 名字 + 描述 整体

	var name string
	var actionDesc string

	// 死亡/复活的分隔符列表 (支持中英文)
	// 关键：在 Death Announce 和 Resurrect Announce 之间寻找共同的分隔模式
	// 中文：死于： / 复活自：
	// 英文：died from / resurrected from / revived by
	announcementWords := []string{
		"死于：", "died from", "was killed by", "starved", "suicide", // 死亡
		"复活自：", "resurrected from", "revived by", // 复活
	}

	splitIndex := -1
	for _, word := range announcementWords {
		// 查找分隔符
		idx := strings.Index(rest, word)
		if idx > splitIndex { // 找到最靠前的已知分隔符
			splitIndex = idx
			// 找到后立即退出循环，因为第一个匹配就是名字和描述的边界
			break
		}
	}

	if splitIndex != -1 {
		// 找到了分隔符：分割 rest
		name = strings.TrimSpace(rest[:splitIndex])
		actionDesc = strings.TrimSpace(rest[splitIndex:])
	} else {
		// 未找到已知分隔符，假设整个 rest 都是名字，描述为空 (适用于名字很长，或系统消息)
		name = rest
		actionDesc = ""
		fmt.Println("Announcement Log 未找到分隔符，将全部分配给 Name:", name)
	}

	spawn := c.getSpawnRole(name)
	connect := c.getConnectInfo(name)
	fmt.Println(connect)

	playerLog := model.PlayerLog{
		Name:        name,
		Role:        spawn.Role,
		Action:      action,
		ActionDesc:  actionDesc,
		Time:        t,
		Ip:          connect.Ip,
		KuId:        connect.KuId,
		SteamId:     connect.SteamId,
		ClusterName: c.clusterName,
	}

	if err := database.Db.Create(&playerLog).Error; err != nil {
		fmt.Println("插入玩家日志失败:", err)
	}
}

func (c *Collect) parseLeave(text string) {
	c.parseJoin(text)
}

func (c *Collect) parseJoin(text string) {
	fmt.Println(text)

	// 正则表达式：捕获 (1)时间, (2)动作, (3)玩家名
	re := regexp.MustCompile(`^\[([^\]]+)\]:\s*(\[[^\]]+\])\s*(.+)$`)
	matches := re.FindStringSubmatch(text)

	// 预期匹配 4 组：[完整匹配, 时间, 动作, 玩家名]
	if len(matches) != 4 {
		log.Println("无法解析 Join Log (正则不匹配):", text)
		return
	}

	// 捕获结果
	t := matches[1]      // 时间: 00:01:43
	action := matches[2] // 动作: [Join Announcement]
	// 擦屁股
	action = strings.ReplaceAll(action, " ", "")
	// 玩家名字是捕获组 3，使用 strings.TrimSpace 确保名字前后没有多余空格
	name := strings.TrimSpace(matches[3])

	spawn := c.getSpawnRole(name)
	connect := c.getConnectInfo(name)

	actionDesc := "玩家加入世界"
	if strings.Contains(action, "Leave") {
		actionDesc = "玩家离开世界"
	}

	playerLog := model.PlayerLog{
		Name:        name,
		Role:        spawn.Role,
		Action:      action,
		ActionDesc:  actionDesc,
		Time:        t,
		Ip:          connect.Ip,
		KuId:        connect.KuId,
		SteamId:     connect.SteamId,
		ClusterName: c.clusterName,
	}

	// 如果 30 秒内刚因客户端认证生成了同一玩家的上线日志，更新其角色与描述，避免重复刷两条
	var recentLog model.PlayerLog
	if strings.Contains(action, "Join") && database.Db.Where("name = ? AND cluster_name = ?", name, c.clusterName).Order("created_at desc").First(&recentLog).Error == nil && recentLog.ID != 0 && time.Since(recentLog.CreatedAt) < 30*time.Second {
		recentLog.ActionDesc = "玩家进入世界"
		if spawn.Role != "" {
			recentLog.Role = spawn.Role
		}
		if connect.SteamId != "" {
			recentLog.SteamId = connect.SteamId
		}
		database.Db.Save(&recentLog)
	} else {
		if err := database.Db.Create(&playerLog).Error; err != nil {
			fmt.Println("插入玩家日志失败:", err)
		}
	}
}

func (c *Collect) tailServerChatLog(fileName string) {
	log.Println("开始采集 path:", fileName)
	config := tail.Config{
		ReOpen:    true,                                 // 重新打开
		Follow:    true,                                 // 是否跟随
		Location:  &tail.SeekInfo{Offset: 0, Whence: 2}, // 从文件的哪个地方开始读
		MustExist: false,                                // 文件不存在不报错
		Poll:      true,
	}
	tails, err := tail.TailFile(fileName, config)
	if err != nil {
		log.Println("文件监听失败", err)
	}
	for {
		select {
		case line, ok := <-tails.Lines:
			if !ok {
				log.Println("文件读取失败", err)
				time.Sleep(time.Second)
			} else {
				text := line.Text
				c.parseChatLog(text)
			}
		case <-c.stop:
			// 结束监听
			err := tails.Stop()
			if err != nil {
				log.Println("tail log 结束失败")
				return
			}
			return
		}
	}
}

func (c *Collect) getSpawnRole(name string) *model.Spawn {
	spawn := new(model.Spawn)
	database.Db.Where("name LIKE ? and cluster_name = ?", "%"+name+"%", c.clusterName).Last(spawn)
	return spawn
}

func (c *Collect) getConnectInfo(name string) *model.Connect {
	connect := new(model.Connect)
	if err := database.Db.Where("name = ? and cluster_name = ?", name, c.clusterName).Last(connect).Error; err == nil && connect.ID != 0 {
		return connect
	}
	database.Db.Where("name LIKE ? and cluster_name = ?", "%"+name+"%", c.clusterName).Last(connect)
	return connect
}

func (c *Collect) parseAnnouncement(text string) {
	fmt.Println(text)

	// 正则解析日志
	re := regexp.MustCompile(`\[(.*?)\]: (\[Announcement\]) (.*)`)
	matches := re.FindStringSubmatch(text)
	// 无法解析宣告日志: 00:55:21 Announcement test
	if len(matches) != 4 {
		fmt.Println("无法解析宣告日志:", text, matches)
		return
	}

	// 时间
	t := matches[1]
	// [Announcement]
	action := matches[2]
	// 宣告的内容
	actionDesc := matches[3]

	playerLog := model.PlayerLog{
		Name:        "-",
		Role:        "-",
		Action:      action,
		ActionDesc:  actionDesc,
		Time:        t,
		Ip:          "-",
		KuId:        "-",
		SteamId:     "-",
		ClusterName: c.clusterName,
	}

	// 保存到数据库，并打印错误
	if err := database.Db.Create(&playerLog).Error; err != nil {
		fmt.Println("插入玩家日志失败:", err)
	}
}
