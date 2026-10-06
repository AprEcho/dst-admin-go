package login

import (
	"dst-admin-go/internal/config"
	"dst-admin-go/internal/pkg/response"
	"dst-admin-go/internal/pkg/utils/fileUtils"
	"fmt"
	"log"
	"net"
	"path/filepath"
	"strings"

	"github.com/gin-contrib/sessions"

	"github.com/gin-gonic/gin"
)

type LoginService struct {
	config *config.Config
}

// passwordPath 账户信息文件的完整路径
func (l *LoginService) passwordPath() string {
	return filepath.Join(l.config.DataDir, "password.txt")
}

type UserInfo struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName"`
	PhotoURL    string `json:"photoURL"`
}

func NewLoginService(config *config.Config) *LoginService {
	return &LoginService{
		config: config,
	}
}

func parsePasswordLines(lines []string) (username, password, displayName, photoURL string) {
	for _, line := range lines {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		switch strings.ToLower(key) {
		case "username":
			username = val
		case "password":
			password = val
		case "displayname":
			displayName = val
		case "photourl":
			photoURL = val
		}
	}
	return
}

func (l *LoginService) GetUserInfo() UserInfo {
	user, err := fileUtils.ReadLnFile(l.passwordPath())
	if err != nil {
		log.Println("Read password file error:", err)
		return UserInfo{Username: "admin"}
	}

	username, _, displayName, photoURL := parsePasswordLines(user)
	if username == "" {
		username = "admin"
	}

	return UserInfo{
		Username:    username,
		DisplayName: displayName,
		PhotoURL:    photoURL,
	}
}

func (l *LoginService) Login(userInfo UserInfo, ctx *gin.Context) *response.Response {

	resp := &response.Response{}

	user, err := fileUtils.ReadLnFile(l.passwordPath())
	if err != nil {
		log.Println("Read password file error:", err)
		resp.Code = 500
		resp.Msg = "读取认证配置失败"
		return resp
	}

	username, password, displayName, photoURL := parsePasswordLines(user)
	white := l.IsWhiteIP(ctx)
	if !white {
		if username != userInfo.Username || password != userInfo.Password {
			resp.Code = 401
			resp.Msg = "User authentication failed"
			return resp
		}
	}
	session := sessions.Default(ctx)
	session.Set("username", username)
	err = session.Save()
	if err != nil {
		log.Println("save session error:", err)
		resp.Code = 500
		resp.Msg = "保存会话失败"
		return resp
	}

	resp.Code = 200
	resp.Msg = "Login success"
	resp.Data = map[string]interface{}{
		"username":    username,
		"displayName": displayName,
		"photoURL":    photoURL,
	}

	return resp
}

func (l *LoginService) Logout(ctx *gin.Context) {
	session := sessions.Default(ctx)
	session.Clear()
	err := session.Save()
	if err != nil {
		log.Println("logout session save error:", err)
	}
}

func (l *LoginService) DirectLogin(ctx *gin.Context) {
	user, err := fileUtils.ReadLnFile(l.passwordPath())
	if err != nil {
		log.Println("Read password file error:", err)
		return
	}
	username, _, _, _ := parsePasswordLines(user)
	if username == "" {
		username = "admin"
	}
	session := sessions.Default(ctx)
	session.Set("username", username)
	_ = session.Save()
}

func (l *LoginService) ChangeUser(username, password string) {
	user, err := fileUtils.ReadLnFile(l.passwordPath())
	displayName := ""
	photoURL := ""
	if err == nil {
		_, _, displayName, photoURL = parsePasswordLines(user)
	}
	fileUtils.CreateDirIfNotExists(l.config.DataDir)
	_ = fileUtils.WriterLnFile(l.passwordPath(), []string{
		"username = " + username,
		"password = " + password,
		"displayName=" + displayName,
		"photoURL=" + photoURL,
	})
}

func (l *LoginService) ChangePassword(newPassword string) *response.Response {

	resp := &response.Response{}
	user, err := fileUtils.ReadLnFile(l.passwordPath())
	if err != nil {
		log.Println("Read password file error:", err)
		resp.Code = 500
		resp.Msg = "读取认证配置失败"
		return resp
	}
	username, _, displayName, photoURL := parsePasswordLines(user)
	if username == "" {
		username = "admin"
	}
	fileUtils.CreateDirIfNotExists(l.config.DataDir)
	err = fileUtils.WriterLnFile(l.passwordPath(), []string{
		"username = " + username,
		"password = " + newPassword,
		"displayName=" + displayName,
		"photoURL=" + photoURL,
	})
	if err != nil {
		log.Println("write password file error:", err)
		resp.Code = 500
		resp.Msg = "写入密码文件失败"
		return resp
	}

	resp.Code = 200
	resp.Msg = "Update user new password success"

	return resp
}

func (l *LoginService) InitUserInfo(userInfo UserInfo) {
	username := "username=" + userInfo.Username
	password := "password=" + userInfo.Password
	displayName := "displayName=" + userInfo.DisplayName
	photoURL := "photoURL=" + userInfo.PhotoURL
	fileUtils.CreateDirIfNotExists(l.config.DataDir)
	_ = fileUtils.WriterLnFile(l.passwordPath(), []string{username, password, displayName, photoURL})
}

func (l *LoginService) IsWhiteIP(ctx *gin.Context) bool {
	if l.config == nil {
		return false
	}
	WhiteAdminIP := l.config.WhiteAdminIP
	if WhiteAdminIP != "" {
		//
		ipaddr := ctx.Request.RemoteAddr
		ip, _, _ := net.SplitHostPort(ipaddr)
		if ip != "" {
			ipnet := net.ParseIP(ip)
			adminips := strings.Split(WhiteAdminIP, ",")
			//fmt.Println(ipnet)
			for _, s := range adminips {
				if strings.Count(s, "/") > 0 {
					_, netadmin, err := net.ParseCIDR(s)
					//fmt.Println(netadmin)
					//fmt.Println(len)
					if err != nil {
						fmt.Printf("Error parsing CIDR: %v\n", err)
					}
					if netadmin.Contains(ipnet) {
						return true
					}
				} else {
					netadmin := net.ParseIP(s)
					if netadmin != nil && netadmin.Equal(ipnet) {
						return true
					}
				}
			}

		}
	}
	return false
}
