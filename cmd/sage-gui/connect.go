package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/l33tdawg/sage/web"
)

// connectProvider is the same-machine one-click connect dispatcher wired into
// the dashboard via DashboardHandler.ConnectFunc (see node.go). It resolves the
// running sage-gui binary + SAGE_HOME, maps the provider id to the matching
// config writer, and returns the list of files touched.
//
// Folder-scoped providers (claude-code, codex, cursor) receive the project dir
// in `path`; app-scoped providers (windsurf, claude-desktop) ignore it. The
// dashboard handler validates provider + path before we get here.
//
// token is only meaningful for claude-code (it claims a pre-configured
// identity). For the other providers a token is accepted but is currently a
// no-op — the agent auto-registers on first connect (same as the CLI without
// --token). Remote (Flow 2) and LAN pairing (Flow 3) are later sub-phases.
func connectProvider(provider, path, token string) ([]web.ConnectFile, error) {
	execPath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("find sage-gui binary: %w", err)
	}
	if resolved, symErr := filepath.EvalSymlinks(execPath); symErr == nil {
		execPath = resolved
	}
	sageHome := SageHome()

	switch provider {
	case "claude-code":
		return installClaudeCodeConfig(path, sageHome, execPath, token)
	case "codex":
		return installCodexConfig(path, sageHome, execPath)
	case "cursor":
		return writeCursorConfig(path, sageHome, execPath)
	case "windsurf":
		return writeWindsurfConfig(sageHome, execPath)
	case "claude-desktop":
		return writeClaudeDesktopConfig(sageHome, execPath)
	default:
		return nil, fmt.Errorf("unsupported provider: %s", provider)
	}
}

// writeCursorConfig registers the sage stdio server in <projectDir>/.cursor/mcp.json
// (folder-scoped). Existing servers are preserved.
func writeCursorConfig(projectDir, sageHome, execPath string) ([]web.ConnectFile, error) {
	path := filepath.Join(projectDir, ".cursor", "mcp.json")
	action, err := mergeMCPServerConfig(path, execPath, sageHome, "cursor")
	if err != nil {
		return nil, err
	}
	return []web.ConnectFile{{Path: path, Action: action}}, nil
}

// writeWindsurfConfig registers the sage stdio server in Windsurf's app-scoped
// MCP config (~/.codeium/windsurf/mcp_config.json). Existing servers are preserved.
func writeWindsurfConfig(sageHome, execPath string) ([]web.ConnectFile, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("get home dir: %w", err)
	}
	path := filepath.Join(home, ".codeium", "windsurf", "mcp_config.json")
	action, err := mergeMCPServerConfig(path, execPath, sageHome, "windsurf")
	if err != nil {
		return nil, err
	}
	return []web.ConnectFile{{Path: path, Action: action}}, nil
}

// writeClaudeDesktopConfig registers the sage stdio server in Claude Desktop's
// app-scoped config at the platform-specific path. Existing servers are preserved.
func writeClaudeDesktopConfig(sageHome, execPath string) ([]web.ConnectFile, error) {
	path, err := claudeDesktopConfigPath()
	if err != nil {
		return nil, err
	}
	action, err := mergeMCPServerConfig(path, execPath, sageHome, "claude-desktop")
	if err != nil {
		return nil, err
	}
	return []web.ConnectFile{{Path: path, Action: action}}, nil
}

// claudeDesktopConfigPath returns the platform-specific claude_desktop_config.json
// location (matches the paths used by handleInstallMCP in wizard.go).
func claudeDesktopConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json"), nil
	case "windows":
		return filepath.Join(os.Getenv("APPDATA"), "Claude", "claude_desktop_config.json"), nil
	default:
		return filepath.Join(home, ".config", "claude", "claude_desktop_config.json"), nil
	}
}

// mergeMCPServerConfig writes (or merges) a single "sage" stdio server entry
// into an MCP-style JSON config file — the mcpServers map shared by Claude
// Code (.mcp.json), Cursor (.cursor/mcp.json), Windsurf, and Claude Desktop.
// Any pre-existing servers (and other top-level keys) are preserved. The parent
// directory is created if needed and the file is written 0600.
//
// Returns "created" when the file did not previously exist, "merged" when an
// existing config was updated.
func mergeMCPServerConfig(path, execPath, sageHome, provider string) (string, error) {
	action := "created"
	config := map[string]any{}

	if data, err := os.ReadFile(path); err == nil { //nolint:gosec // path composed from project/home dirs, not remote input
		action = "merged"
		if len(strings.TrimSpace(string(data))) > 0 {
			if jsonErr := json.Unmarshal(data, &config); jsonErr != nil {
				return "", fmt.Errorf("existing config has invalid JSON — edit or remove it manually: %s", path)
			}
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read %s: %w", path, err)
	}

	servers, ok := config["mcpServers"].(map[string]any)
	if !ok {
		servers = map[string]any{}
	}
	servers["sage"] = map[string]any{
		"command": execPath,
		"args":    []string{"mcp"},
		"env": map[string]string{
			"SAGE_HOME":     sageHome,
			"SAGE_PROVIDER": provider,
		},
	}
	config["mcpServers"] = servers

	if mkErr := os.MkdirAll(filepath.Dir(path), 0755); mkErr != nil { //nolint:gosec // parent dir is under project/home
		return "", fmt.Errorf("create config dir: %w", mkErr)
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal config: %w", err)
	}
	if writeErr := safeWriteFile(path, append(data, '\n'), 0600); writeErr != nil {
		return "", fmt.Errorf("write %s: %w", path, writeErr)
	}
	return action, nil
}

// safeWriteFile writes data to path with perm, but refuses to write THROUGH a
// symlink at the final path component. The target sits under an operator-
// supplied directory, so a pre-planted symlink at a fixed config path could
// otherwise redirect the write onto an arbitrary file it points at. Best-
// effort: it does not chase intermediate directory symlinks (same-machine,
// authenticated threat model keeps that residual low).
func safeWriteFile(path string, data []byte, perm os.FileMode) error {
	if fi, lerr := os.Lstat(path); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to write through a symlink: %s", path)
	}
	return os.WriteFile(path, data, perm) //nolint:gosec // caller-composed local path; symlink refused above
}

// fileAction reports "merged" when path already exists (an existing config is
// being updated) or "created" when it does not — used to label ConnectFile
// entries for files written by helpers that don't themselves distinguish the
// two (installClaudeHooks, installClaudeMD, installAgentsMD, writeCodexConfig).
// Call it BEFORE the write.
func fileAction(path string) string {
	if _, err := os.Stat(path); err == nil {
		return "merged"
	}
	return "created"
}
