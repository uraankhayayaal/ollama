package sandbox

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

func TestLoadConfigSessionDefaultsToIsolation(t *testing.T) {
	t.Setenv("CODEGEN_SANDBOX", "session")
	t.Setenv("CODEGEN_SANDBOX_NETWORK", "")
	t.Setenv("CODEGEN_SANDBOX_ALLOW_DOMAINS", "")
	cfg := LoadConfig()
	if cfg.Mode != ModeSession {
		t.Errorf("Mode = %q, ожидался session", cfg.Mode)
	}
	// П. 1.10: без явной сети сессия изолирована по умолчанию.
	if cfg.Network != "none" {
		t.Errorf("Network = %q, для сессии ожидался none", cfg.Network)
	}
}

func TestLoadConfigSessionWhitelistFromDomains(t *testing.T) {
	t.Setenv("CODEGEN_SANDBOX", "session")
	t.Setenv("CODEGEN_SANDBOX_NETWORK", "")
	t.Setenv("CODEGEN_SANDBOX_ALLOW_DOMAINS", "proxy.golang.org, *.npmjs.org")
	cfg := LoadConfig()
	if cfg.Network != "whitelist" {
		t.Errorf("Network = %q, ожидался whitelist при заданном белом списке", cfg.Network)
	}
	if !reflect.DeepEqual(cfg.AllowDomains, []string{"proxy.golang.org", "*.npmjs.org"}) {
		t.Errorf("AllowDomains = %v", cfg.AllowDomains)
	}
}

func TestLoadConfigExplicitNetworkWinsAndWhitelistRollsBack(t *testing.T) {
	// Явный CODEGEN_SANDBOX_NETWORK отменяет изоляцию по умолчанию (opt-out).
	t.Setenv("CODEGEN_SANDBOX", "session")
	t.Setenv("CODEGEN_SANDBOX_NETWORK", "default")
	t.Setenv("CODEGEN_SANDBOX_ALLOW_DOMAINS", "proxy.golang.org")
	if cfg := LoadConfig(); cfg.Network != "default" {
		t.Errorf("явный default должен побеждать, получено %q", cfg.Network)
	}

	// whitelist вне session не поддержан (прокси живёт с контейнером) — откат.
	t.Setenv("CODEGEN_SANDBOX", "container")
	t.Setenv("CODEGEN_SANDBOX_NETWORK", "whitelist")
	if cfg := LoadConfig(); cfg.Network != "default" {
		t.Errorf("whitelist вне session должен откатиться в default, получено %q", cfg.Network)
	}

	// whitelist без единого домена — нечего пускать: none.
	t.Setenv("CODEGEN_SANDBOX", "session")
	t.Setenv("CODEGEN_SANDBOX_NETWORK", "whitelist")
	t.Setenv("CODEGEN_SANDBOX_ALLOW_DOMAINS", "  ")
	if cfg := LoadConfig(); cfg.Network != "none" {
		t.Errorf("whitelist без доменов должен стать none, получено %q", cfg.Network)
	}
}

func TestLoadConfigDefaultsAreLocal(t *testing.T) {
	t.Setenv("CODEGEN_SANDBOX", "")
	t.Setenv("CODEGEN_SANDBOX_NETWORK", "")
	t.Setenv("CODEGEN_SANDBOX_ALLOW_DOMAINS", "")
	cfg := LoadConfig()
	if cfg.Mode != ModeLocal {
		t.Errorf("по умолчанию ожидается хост, получено %q", cfg.Mode)
	}
	if cfg.Network != "default" {
		t.Errorf("Network по умолчанию = %q, ожидался default", cfg.Network)
	}
	if cfg.Memory != "2g" || cfg.CPUs != "2" {
		t.Errorf("лимиты по умолчанию: %s/%s", cfg.Memory, cfg.CPUs)
	}
}

func TestFirstEnvAndTmpAndHostUser(t *testing.T) {
	t.Setenv("SBOX_T1", "")
	t.Setenv("SBOX_T2", " value ")
	if got := FirstEnv("SBOX_T1", "SBOX_T2"); got != "value" {
		t.Errorf("FirstEnv = %q, ожидался value (трим)", got)
	}
	if Tmp != "/tmp" {
		t.Errorf("Tmp = %q: HOME и кэши обязаны быть в /tmp", Tmp)
	}
	env := BaseEnv()
	if env["HOME"] != Tmp || env["GOCACHE"] == "" {
		t.Errorf("BaseEnv неполон: %v", env)
	}
	if u := HostUser(); u != "" && (u[0] < '0' || u[0] > '9') {
		t.Errorf("HostUser = %q, ожидался uid:gid", u)
	}
}

func TestLocalWorkspaceRunAndTimeout(t *testing.T) {
	ws := &LocalWorkspace{}
	res, err := ws.Run(context.Background(), t.TempDir(), "echo hi")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 || res.Stdout == "" || res.TimedOut {
		t.Errorf("неожиданный результат: %+v", res)
	}

	// Ненулевой выход — это Result, а не ошибка.
	res, err = ws.Run(context.Background(), t.TempDir(), "exit 3")
	if err != nil {
		t.Fatalf("ненулевой выход не должен быть ошибкой: %v", err)
	}
	if res.ExitCode != 3 || res.ExitErr == nil {
		t.Errorf("ожидался ExitCode=3 с ExitErr, получено %+v", res)
	}

	// Таймаут: команда убивается, TimedOut=true.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	res, err = ws.Run(ctx, t.TempDir(), "sleep 5")
	if err != nil {
		t.Fatalf("Run с таймаутом: %v", err)
	}
	if !res.TimedOut {
		t.Errorf("ожидался TimedOut, получено %+v", res)
	}
}

func TestLocalWorkspaceHookReplacesShell(t *testing.T) {
	dir := t.TempDir()
	var gotCmd, gotDir string
	ws := &LocalWorkspace{Command: func(command, workdir string) (*exec.Cmd, error) {
		gotCmd, gotDir = command, workdir
		return exec.Command("sh", "-c", "true"), nil
	}}
	if _, err := ws.Run(context.Background(), dir, "custom cmd"); err != nil {
		t.Fatal(err)
	}
	if gotCmd != "custom cmd" || gotDir != dir {
		t.Errorf("хук получил %q/%q", gotCmd, gotDir)
	}
	// Ошибка хука — ошибка запуска, а не Result.
	ws.Command = func(string, string) (*exec.Cmd, error) { return nil, errors.New("нет") }
	if _, err := ws.Run(context.Background(), dir, "x"); err == nil {
		t.Error("ошибка хука должна возвращаться как ошибка Run")
	}
	if err := ws.Close(context.Background()); err != nil {
		t.Errorf("Close хоста: %v", err)
	}
}
