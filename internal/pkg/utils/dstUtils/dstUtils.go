package dstUtils

import (
	"bytes"
	"dst-admin-go/internal/pkg/utils/fileUtils"
	"dst-admin-go/internal/service/dstConfig"
	"io/ioutil"
	"log"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	textTemplate "text/template"
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
	var enabledIds []string
	re := regexp.MustCompile(`\["workshop-(\w[-\w+]*)"\]\s*=\s*\{([^}]+)\}`)
	matches := re.FindAllStringSubmatch(content, -1)
	if len(matches) > 0 {
		for _, m := range matches {
			id := m[1]
			body := m[2]
			if regexp.MustCompile(`enabled\s*=\s*false`).MatchString(body) {
				continue
			}
			enabledIds = append(enabledIds, id)
		}
		return enabledIds
	}
	return WorkshopIds(content)
}

func DedicatedServerModsSetup(dstConfig dstConfig.DstConfig, modConfig string) error {
	if modConfig != "" {
		var serverModSetup []string
		workshopIds := WorkshopIds(modConfig)
		for _, workshopId := range workshopIds {
			serverModSetup = append(serverModSetup, "ServerModSetup(\""+workshopId+"\")")
		}
		modSetupPath := GetModSetup2(dstConfig)
		mods, err := fileUtils.ReadLnFile(modSetupPath)
		if err != nil {
			return err
		}
		var newServerModSetup []string
		for i := range serverModSetup {
			var notFind = true
			for j := range mods {
				if serverModSetup[i] == mods[j] {
					notFind = false
					break
				}
			}
			if notFind {
				newServerModSetup = append(newServerModSetup, serverModSetup[i])
			}
		}
		newServerModSetup = append(newServerModSetup, mods...)
		err = fileUtils.WriterLnFile(modSetupPath, newServerModSetup)
		if err != nil {
			return err
		}
	}
	return nil
}

func GetModSetup2(dstConfig dstConfig.DstConfig) string {
	return filepath.Join(dstConfig.Force_install_dir, "mods", "dedicated_server_mods_setup.lua")
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
