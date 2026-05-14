package mcp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCurrentBranchTag_NotAGitRepo(t *testing.T) {
	resetBranchCache()
	tmp := t.TempDir()
	// chdir into a fresh tempdir that is NOT a git repo
	oldWd, _ := os.Getwd()
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir tmp: %v", err)
	}

	tag := currentBranchTag(context.Background())
	if tag != "" {
		t.Fatalf("expected empty tag outside a git repo, got %q", tag)
	}
}

func TestCurrentBranchTag_DisabledViaEnv(t *testing.T) {
	resetBranchCache()
	t.Setenv("SAGE_BRANCH_TAG", "0")
	tag := currentBranchTag(context.Background())
	if tag != "" {
		t.Fatalf("expected empty tag when disabled via env, got %q", tag)
	}
}

func TestCurrentBranchTag_DetectsBranchInRealRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	resetBranchCache()
	t.Setenv("SAGE_BRANCH_TAG", "")

	tmp := t.TempDir()
	oldWd, _ := os.Getwd()
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = tmp
		// keep git from prompting / using user config
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_SYSTEM=/dev/null",
			"HOME="+tmp,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-b", "v7.0-test-branch")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")

	// commit at least once so HEAD resolves
	if err := os.WriteFile(filepath.Join(tmp, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	run("add", "f.txt")
	run("commit", "-m", "init")

	tag := currentBranchTag(context.Background())
	if !strings.HasPrefix(tag, "branch:") {
		t.Fatalf("expected branch:<name> tag, got %q", tag)
	}
	if !strings.Contains(tag, "v7.0-test-branch") {
		t.Fatalf("expected tag to carry the actual branch name, got %q", tag)
	}
}
