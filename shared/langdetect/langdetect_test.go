package langdetect

import "testing"

func TestDetect(t *testing.T) {
	cases := []struct {
		name string
		diff string
		want Language
	}{
		{
			name: "php",
			diff: "diff --git a/src/app/Http/Controllers/TodoController.php b/...\n" +
				"+++ b/src/app/Http/Controllers/TodoController.php\n@@ -1,3 +1,4 @@\n",
			want: PHP,
		},
		{
			name: "go",
			diff: "+++ b/internal/service/service.go\n",
			want: Go,
		},
		{
			name: "ts преобладает над прочими",
			diff: "+++ b/src/index.ts\n+++ b/src/utils.ts\n+++ b/readme.md\n",
			want: TypeScript,
		},
		{
			name: "sql",
			diff: "+++ b/db/migrations/0001_users.sql\n",
			want: SQL,
		},
		{
			name: "неизвестные расширения",
			diff: "+++ b/config.ini\n+++ b/data.bin\n+++ b/file.xyz\n",
			want: Unknown,
		},
		{
			name: "удаление файла игнорируется",
			diff: "--- a/old.txt\n+++ b/old.txt (deleted)\n",
			want: Unknown,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Detect(c.diff); got != c.want {
				t.Errorf("Detect() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestReviewPromptHasLanguageSpecifics(t *testing.T) {
	phpPrompt := ReviewPrompt(PHP, "")
	if !contains(phpPrompt, "PSR-12") {
		t.Error("PHP промпт должен упоминать PSR-12")
	}
	generic := ReviewPrompt(Unknown, "")
	if contains(generic, "PSR-12") {
		t.Error("универсальный промпт не должен содержать PHP-специфику")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
