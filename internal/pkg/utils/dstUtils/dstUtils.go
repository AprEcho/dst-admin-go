package dstUtils

import (
	"bytes"
	"dst-admin-go/internal/pkg/utils/fileUtils"
	"dst-admin-go/internal/service/dstConfig"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	textTemplate "text/template"

	lua "github.com/yuin/gopher-lua"
)

func EscapePath(path string) string {
	if runtime.GOOS == "windows" {
		return path
	}
	// 在这里添加需要转义的特殊字符
	escapedChars := []string{" ", "'", "(", ")"}
	for _, char := range escapedChars {
		path = strings.ReplaceAll(path, char, "\\"+char)
	}
	return path
}

func WorkshopIds(content string) []string {
	var workshopIds []string

	re := regexp.MustCompile("\"workshop-\\w[-\\w+]*\"")
	workshops := re.FindAllString(content, -1)

	for _, workshop := range workshops {
		workshop = strings.Replace(workshop, "\"", "", -1)
		split := strings.Split(workshop, "-")
		workshopId := strings.TrimSpace(split[1])
		workshopIds = append(workshopIds, workshopId)
	}
	return workshopIds
}

// EnabledWorkshopIds 获取仅处于启用状态（enabled != false）的 workshop ID 列表
func EnabledWorkshopIds(content string) []string {
	if strings.TrimSpace(content) == "" {
		return nil
	}

	// 1. 优先使用 gopher-lua 准确解析 Lua table 树结构
	ids, err := parseEnabledWorkshopIdsLua(content)
	if err == nil {
		return ids
	}

	// 2. Fallback: 使用基于大括号平衡匹配解析
	return fallbackParseEnabledWorkshopIds(content)
}

func parseEnabledWorkshopIdsLua(content string) ([]string, error) {
	L := lua.NewState()
	defer L.Close()

	script := strings.TrimSpace(content)
	if !strings.HasPrefix(script, "return") {
		script = "return " + script
	}

	if err := L.DoString(script); err != nil {
		return nil, err
	}

	tbl, ok := L.Get(-1).(*lua.LTable)
	if !ok {
		return nil, errors.New("lua return value is not a table")
	}

	var enabledIds []string
	tbl.ForEach(func(k, v lua.LValue) {
		kStr := k.String()
		if !strings.HasPrefix(kStr, "workshop-") {
			return
		}
		id := strings.TrimSpace(strings.TrimPrefix(kStr, "workshop-"))
		if id == "" {
			return
		}

		if modTbl, ok := v.(*lua.LTable); ok {
			enVal := modTbl.RawGetString("enabled")
			if enVal != lua.LNil {
				if b, ok := enVal.(lua.LBool); ok && !bool(b) {
					// 明确设置为 enabled = false，则忽略
					return
				}
			}
		}
		enabledIds = append(enabledIds, id)
	})
	sort.Strings(enabledIds)
	return enabledIds, nil
}

func fallbackParseEnabledWorkshopIds(content string) []string {
	var enabledIds []string
	re := regexp.MustCompile(`\["workshop-(\w[-\w+]*)"\]\s*=\s*\{`)
	indices := re.FindAllStringSubmatchIndex(content, -1)
	for _, idx := range indices {
		if len(idx) < 4 {
			continue
		}
		modId := content[idx[2]:idx[3]]
		braceStart := idx[1] - 1
		braceCount := 1
		blockEnd := -1
		for i := braceStart + 1; i < len(content); i++ {
			if content[i] == '{' {
				braceCount++
			} else if content[i] == '}' {
				braceCount--
				if braceCount == 0 {
					blockEnd = i
					break
				}
			}
		}

		var block string
		if blockEnd != -1 {
			block = content[braceStart:blockEnd]
		} else {
			block = content[braceStart:]
		}

		if regexp.MustCompile(`enabled\s*=\s*false`).MatchString(block) {
			continue
		}
		enabledIds = append(enabledIds, modId)
	}
	sort.Strings(enabledIds)
	return enabledIds
}

// DedicatedServerModsSetup 兼容旧接口，对传入的 modConfig 执行同步
func DedicatedServerModsSetup(dstConfig dstConfig.DstConfig, modConfig string) error {
	return SyncDedicatedServerModsSetup(dstConfig, modConfig)
}

// SyncDedicatedServerModsSetup 根据传入的活跃世界 modoverrides 配置，
// 将实际处于启用状态（enabled != false）的模组同步到 dedicated_server_mods_setup.lua。
// 未启用（enabled = false）或已删除的模组将从清单中彻底剔除，避免服务器启动时盲目下载。
func SyncDedicatedServerModsSetup(dstConfig dstConfig.DstConfig, modConfigs ...string) error {
	modSetupPath := GetModSetup2(dstConfig)
	if modSetupPath == "" {
		return nil
	}

	// 1. 汇总所有传入配置中实际处于启用的 workshop ID
	enabledMap := make(map[string]bool)
	for _, cfg := range modConfigs {
		for _, id := range EnabledWorkshopIds(cfg) {
			id = strings.TrimSpace(id)
			if id != "" {
				enabledMap[id] = true
			}
		}
	}

	// 2. 读取现有 dedicated_server_mods_setup.lua 中的行
	var existingLines []string
	if fileUtils.Exists(modSetupPath) {
		lines, err := fileUtils.ReadLnFile(modSetupPath)
		if err == nil {
			existingLines = lines
		}
	}

	// 3. 同步过滤：
	// 如果是 ServerModSetup("xxx")：
	//   若 xxx 属于 enabledMap，保留并标记已写入；
	//   若 xxx 不属于 enabledMap（已禁用或已被用户删除），彻底剔除！
	// 其他行（如注释、空行、自定义指令）原样保留。
	serverModRe := regexp.MustCompile(`^\s*ServerModSetup\s*\(\s*["'](\w[-\w+]*)["']\s*\)`)
	var newLines []string
	written := make(map[string]bool)

	for _, line := range existingLines {
		trimmed := strings.TrimSpace(line)
		match := serverModRe.FindStringSubmatch(trimmed)
		if len(match) == 2 {
			id := match[1]
			if enabledMap[id] {
				if !written[id] {
					newLines = append(newLines, fmt.Sprintf("ServerModSetup(\"%s\")", id))
					written[id] = true
				}
			}
			// 不在 enabledMap 中，说明未启用或已被删除，剔除（不追加到 newLines）
		} else {
			newLines = append(newLines, line)
		}
	}

	// 4. 追加原本不在文件中、但新启用的模组（按 ID 排序保持稳定性）
	var pendingNewIds []string
	for id := range enabledMap {
		if !written[id] {
			pendingNewIds = append(pendingNewIds, id)
		}
	}
	sort.Strings(pendingNewIds)
	for _, id := range pendingNewIds {
		newLines = append(newLines, fmt.Sprintf("ServerModSetup(\"%s\")", id))
	}

	dir := filepath.Dir(modSetupPath)
	if !fileUtils.Exists(dir) {
		fileUtils.CreateDir(dir)
	}

	return fileUtils.WriterLnFile(modSetupPath, newLines)
}

func GetModSetup2(dstConfig dstConfig.DstConfig) string {
	dstServerPath := dstConfig.Force_install_dir
	if dstConfig.Beta == 1 {
		dstServerPath = dstServerPath + "-beta"
	}
	return filepath.Join(dstServerPath, "mods", "dedicated_server_mods_setup.lua")
}

func ParseTemplate(templatePath string, data interface{}) string {

	// 读取文件内容
	content, err := ioutil.ReadFile(templatePath)
	if err != nil {
		log.Println("read template failed:", templatePath, err)
		return ""
	}

	// 创建模板对象
	tmpl, err := textTemplate.New("myTemplate").Parse(string(content))
	if err != nil {
		log.Println("parse template failed:", err)
		return ""
	}

	// 执行模板并保存结果到字符串
	buf := new(bytes.Buffer)
	err = tmpl.Execute(buf, data)
	if err != nil {
		log.Println("execute template failed:", err)
		return ""
	}
	return buf.String()

}
