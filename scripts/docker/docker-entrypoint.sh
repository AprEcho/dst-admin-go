#!/bin/bash

# 修正最大文件描述符数，部分docker版本给的默认值过高，会导致screen运行卡顿
ulimit -Sn 10000

# 获取传入的参数
steam_cmd_path='/app/steamcmd'
steam_dst_server='/app/dst-dedicated-server'

mkdir -p "$steam_cmd_path"
mkdir -p /root/.klei/DoNotStarveTogether/MyDediServer
mkdir -p /app/backup
mkdir -p /app/mod

# 默认配置文件
if [ ! -f /app/dst_config ]; then
  if [ -f /app/docker_dst_config.default ]; then
    cp /app/docker_dst_config.default /app/dst_config
  elif [ -f /app/docker_dst_config ]; then
    cp /app/docker_dst_config /app/dst_config
  fi
fi

# 初始管理员账户文件
if [ ! -f /app/password.txt ]; then
  echo "username=admin" >> /app/password.txt
  echo "password=123456" >> /app/password.txt
  echo "displayName=admin" >> /app/password.txt
  echo "photoURL=xxx" >> /app/password.txt
fi

# 进入 steam_cmd_path 目录
cd "$steam_cmd_path"

retry=1
while [ ! -d "${steam_cmd_path}" ] || [ ! -e "${steam_cmd_path}/steamcmd.sh" ]; do
  if [ $retry -gt 3 ]; then
    echo "Download steamcmd failed after three times"
    exit -2
  fi
  echo "Not found steamcmd, start to installing steamcmd, try: ${retry}"
  wget http://media.steampowered.com/installer/steamcmd_linux.tar.gz -P $steam_cmd_path
  tar -zxvf $steam_cmd_path/steamcmd_linux.tar.gz -C $steam_cmd_path
  sleep 3
  ((retry++))
done

retry=1
while [ ! -e "${steam_dst_server}/bin/dontstarve_dedicated_server_nullrenderer" ]; do
  if [ $retry -gt 3 ]; then
    echo "Download Dont Starve Together Sever failed after three times"
    exit -2
  fi
  echo "Not found Dont Starve Together Sever, start to installing, try: ${retry}"
  bash $steam_cmd_path/steamcmd.sh +force_install_dir $steam_dst_server +login anonymous +app_update 343050 validate +quit
  sleep 3
  ((retry++))
done

echo "SteamCMD installed at $steam_cmd_path"
echo "SteamDST server installed at $steam_dst_server"

cd /app
exec ./dst-admin-go
