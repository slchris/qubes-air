// Qubes Air Console - qrexec Client
//
// 通过 qrexec 与 sys-remote 通信

package qrexec

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

// Client qrexec 客户端
type Client struct {
	timeout time.Duration
}

// NewClient 创建新的 qrexec 客户端
func NewClient() *Client {
	return &Client{
		timeout: 30 * time.Second,
	}
}

// Call 调用 qrexec 服务
func (c *Client) Call(ctx context.Context, target, service string, input []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	// 构建命令: qrexec-client-vm <target> <service>
	cmd := exec.CommandContext(ctx, "qrexec-client-vm", target, service)

	// 设置输入
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}

	// 捕获输出
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// 执行
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("qrexec call failed: %v, stderr: %s", err, stderr.String())
	}

	return stdout.Bytes(), nil
}

// RemoteExec 在远程 Zone 执行命令
func (c *Client) RemoteExec(ctx context.Context, sysRemote, command string) ([]byte, error) {
	return c.Call(ctx, sysRemote, "qubes-air.Remote", []byte(command))
}

// GetStatus 获取 sys-remote 状态
func (c *Client) GetStatus(ctx context.Context, sysRemote string) (*SysRemoteStatus, error) {
	output, err := c.Call(ctx, sysRemote, "qubes-air.Status", nil)
	if err != nil {
		return nil, err
	}

	// 解析状态 (简化版)
	return &SysRemoteStatus{
		Connected: true,
		RawOutput: string(output),
	}, nil
}

// SysRemoteStatus sys-remote 状态
type SysRemoteStatus struct {
	Connected bool   `json:"connected"`
	VPNStatus string `json:"vpn_status"`
	RawOutput string `json:"raw_output"`
}
