# 连接第二台 Windows 电脑

推荐使用 [同一个 EXE 的页面接入](WINDOWS-PAIRING.md)。本篇仅供需要使用旧版命令行的维护人员。演示窗口用的 `127.0.0.1` 只指当前电脑，不能拿来配另一台。假设运行 Hub 的电脑局域网 IP 是 `192.168.1.20`；请换成实际固定 IP 或可解析的内网域名。先用专用 NTFS 测试目录，不把业务目录加入授权。

## 1. 在 Hub 电脑启动内网实例

构建 `hub.exe`、`agent.exe`，见 [Windows 试运行](START-WINDOWS.md)。在 Hub 电脑使用独立的新状态目录；`init` 的目标目录须不存在。下面的 `C:\Dockroom` 是示例父目录：

```powershell
Set-Location C:\Dockroom
.\hub.exe init --data C:\Dockroom\Hub-LAN --host 192.168.1.20
.\hub.exe user-add --data C:\Dockroom\Hub-LAN --user reader
.\hub.exe serve --data C:\Dockroom\Hub-LAN --origin https://192.168.1.20:8442 --listen 192.168.1.20:8442 --device-listen 192.168.1.20:8443
```

初始化时在终端输入自设的口令。`--host` 必须与浏览器和 Agent 实际使用的地址一致，因为它写入了 TLS 证书。不要重用 `localhost` 演示的证书。Hub 端的 TCP **8442**（工作台和首次接入）和 **8443**（Agent）只向可信局域网或 VPN 开放，不做公网端口转发。另一台电脑不需要开放入站端口，也不用启用共享文件夹。可在另一台上先运行 `Test-NetConnection 192.168.1.20 -Port 8442` 和 `Test-NetConnection 192.168.1.20 -Port 8443` 检查连通。

浏览器验证该 Hub 的 HTTPS 证书。在受控试验环境中，由管理员将经过核对的本次 Hub 公共 `ca.pem` 加入用于试连的受信任证书存储；不要无视证书错误。登录 `https://192.168.1.20:8442` 的管理员账号，进入「设备管理」。

## 2. 在另一台电脑运行 Agent

只复制同版本 `agent.exe`，以及经过核对的 Hub 公共 `ca.pem`。不要复制 Hub 数据库、`ca-key.pem`、`server-key.pem`，也不要复制其他 Agent 的证书或私钥。

以 `C:\Dockroom\Agent-PC2` 为 Agent 状态目录。新建一个专用 NTFS 测试根，例如 `D:\Dockroom-Test`（没有 D 盘可改用实际 NTFS 路径）。执行：

```powershell
Set-Location C:\Dockroom
.\agent.exe pin --path D:\Dockroom-Test
```

将输出填入 `C:\Dockroom\Agent-PC2\config.json`，与公共 `ca.pem` 放在同一目录：

```json
{
  "nodeId": "pc-02",
  "hub": "https://192.168.1.20:8442",
  "control": "wss://192.168.1.20:8443",
  "roots": [{
    "rootId": "test", "label": "第二台电脑", "mode": "readOnly",
    "path": "D:\\Dockroom-Test", "identity": "填 pin 输出"
  }]
}
```

在 Hub 的「设备管理 → 兼容旧版命令行 Agent」为 `pc-02` 生成一次性旧版接入码，然后在第二台电脑执行：

```powershell
.\agent.exe enroll --data C:\Dockroom\Agent-PC2
# 输入接入码；把打印的指纹前 12 位与管理页核对码比较，再批准。
.\agent.exe serve --data C:\Dockroom\Agent-PC2
```

设备在线后，在管理页给操作者授予 `pc-02 / test` 根的读取权限，切到该账号登录，左右栏的设备下拉即可选择它。如果想把 Hub 所在电脑也作为文件端，需要在那台电脑再运行一个单独的 Agent，使用不同的 `nodeId` 和独立状态目录；Hub 本身不是文件端。

## 3. 需要往另一台写文件时

先确认只读浏览正常。再按 [Windows 写根步骤](START-WINDOWS.md) 配置目标根的 NTFS ACL，执行 `agent prepare-write --path D:\Dockroom-Test`，把输出作为该根的 `privateId`，将 `mode` 改为 `readWrite` 后重启 Agent。管理员再为操作者选择「授予读取及复制写入」。没有通过本地安全检查或没有写授权的根仍然只读。

目前是单文件试运行。已经验证一台 Windows 宿主上的两个独立 Agent；两台独立物理机的连通、防火墙与服务部署要在实际网络中试跑。
