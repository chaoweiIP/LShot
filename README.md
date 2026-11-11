# LShot

LShot 是一个手动滚动长截图工具。框选页面后，在原应用内向下滚动，LShot 会把连续画面拼接并保存为无损 PNG。截图在本机处理。

## macOS

支持 macOS 15.2 及以上。Apple Silicon 和 Intel 共用一个应用。启动后，点菜单栏图标，选择「开始框选」。首次截图时，按 macOS 提示授权屏幕录制。框选后手动滚动，再从菜单选择「结束并保存 PNG」。

在装有 Xcode 命令行工具的 Mac 上运行 `zsh build.sh` 可生成 `LShot.app`。

## Windows 预览版

启动后按窗口内的提示框选、滚动和保存。Windows 版尚未完成真机端到端测试。

使用 Go 1.23 或更新版本，在 macOS 或 Linux 上交叉构建：

```sh
cd Windows
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-H=windowsgui" -o LShot-Windows-x64.exe .
GOOS=windows GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-H=windowsgui" -o LShot-Windows-ARM64.exe .
```

## 许可

源码采用 MIT 许可证。LShot 图标包含「落日梦想家」品牌标识，品牌标识不在代码许可证的授权范围内。
