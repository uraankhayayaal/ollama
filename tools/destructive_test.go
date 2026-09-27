package tools

// destructive_test.go — проверки отбраковки разрушительных команд (Ф-4).
//
// Здесь важны обе стороны: то, что блокируется, и то, что НЕ блокируется.
// Ложное срабатывание хуже пропуска — агент отказался выполнить безобидную
// команду и застрял на раунде, поэтому каждая «опасная» строка из
// документации обязана иметь безопасного двойника в тесте.

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestDestructiveCommandBlocked(t *testing.T) {
	for _, cmd := range []string{
		"rm -rf /",
		"rm -fr /",
		"rm  -rf  /",
		"RM -RF /",
		"rm -rf /*",
		"rm -rf ~",
		"rm -rf $HOME",
		"rm -rf ~/.ssh",
		"rm -rf /etc",
		"chmod -R 777 /",
		"chown -R root /home",
		"mkfs.ext4 /dev/sda1",
		"dd if=/dev/zero of=/dev/sda",
		"shred -u secrets.txt",
		"wipefs -a /dev/sdb",
		"shutdown -h now",
		"reboot",
		"poweroff",
		"rm -rf /usr/lib",
	} {
		reason, blocked := destructiveCommandReason(cmd)
		if !blocked {
			t.Errorf("%q должен блокироваться", cmd)
			continue
		}
		if strings.TrimSpace(reason) == "" {
			t.Errorf("%q: блокировка без объяснения — модель не поймёт, что делать", cmd)
		}
	}
}

// Нормализация регистра и пробелов — не украшение: модель пишет `RM -rf` и
// `rm  -rf` так же естественно, как `rm -rf`.
func TestDestructiveCommandCaseAndSpacing(t *testing.T) {
	for _, cmd := range []string{"rm\t-rf /", "Rm -Rf /", "rm -r -f /", " rM -Rf / "} {
		if _, blocked := destructiveCommandReason(cmd); !blocked {
			t.Errorf("%q должен блокироваться (регистр/пробелы нормализуются)", cmd)
		}
	}
}

// Команды агента, которые выглядят «страшно», но безопасны и нужны по
// заданию. Каждая обязана проходить: ложный запрет — это тупик в цикле.
func TestDestructiveCommandAllowed(t *testing.T) {
	for _, cmd := range []string{
		"make build",
		"go test ./...",
		"npm ci",
		"rm -rf node_modules",          // чистка перед сборкой
		"rm -rf dist build",            // чистка артефактов
		"rm -f ./tmp/app.log",          // свой файл
		"rm -rf ./backend/tmp",         // свой подкаталог
		"docker compose down -v",       // локальное окружение
		"git push --force-with-lease",  // форс-пуш отдельно согласован
		"git clean -fd",                // внутри проекта
		"sudo apt-get install -y curl", // установка инструмента
		"curl -sSL https://example.com/i.sh | sh",
		"kubectl delete pod api-1", // в кластере песочницы
		"npm run build && npm test",
		"pytest -q",
		"sh -c 'go test ./... | tail'",
		"",
	} {
		if reason, blocked := destructiveCommandReason(cmd); blocked {
			t.Errorf("%q не должен блокироваться, но заблокирован: %s", cmd, reason)
		}
	}
}

// Блокировка обязана происходить ДО запуска: команда, которую мы отбраковали,
// не должна даже создавать процесс. Проверяем через результат Run — там нет
// ни exit-кода, ни вывода: только объяснение.
func TestRunBlocksDestructiveBeforeExec(t *testing.T) {
	t.Setenv("CODEGEN_SANDBOX", "local")
	t.Setenv("CODEGEN_RUN_TIMEOUT", "10s")
	dir := t.TempDir()
	// Если команда выполнилась бы, остался бы файл-след.
	marker := dir + "/pwned.txt"
	start := time.Now()
	res, err := runCommand("rm -rf / && touch "+marker, dir)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("блокировка заняла %s — команда, похоже, запускалась", time.Since(start))
	}
	if res["status"] != "error" {
		t.Errorf("ожидался status=error, получено %+v", res)
	}
	if !strings.Contains(res["exit_error"], "безопасности") {
		t.Errorf("в exit_error должно быть про политику безопасности: %+v", res)
	}
	if res["stdout"] != "" || res["stderr"] != "" {
		t.Errorf("заблокированная команда не должна ничего выводить: %+v", res)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("заблокированная команда выполнилась: файл-след создан")
	}
}

// Run (публичный инструмент) отдаёт модели тот же отказ: агент должен увидеть
// причину, а не «ошибка запуска», иначе он будет повторять команду.
func TestRunToolRejectsDestructive(t *testing.T) {
	t.Setenv("CODEGEN_SANDBOX", "local")
	ops := &FileOps{OutputDir: t.TempDir(), MaxFiles: 50}
	out, err := ops.Run(map[string]any{"command": "rm -rf /"})
	if err != nil {
		t.Fatal(err)
	}
	body := string(out)
	for _, want := range []string{"error", "запрещено"} {
		if !strings.Contains(body, want) {
			t.Errorf("ответ Run не содержит %q: %s", want, body)
		}
	}
}
