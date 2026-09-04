// Package mrlistener следит за появлением новых Merge Request в GitLab:
// периодически опрашивает проекты и сообщает о незнакомых MR-ах.
// Запускается подкомандой CLI: go run . listen.
package mrlistener

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// pageSize — сколько MR запрашивается за раз у GitLab API.
const pageSize = 100

// MergeRequest — запись из GitLab API /projects/:id/merge_requests.
type MergeRequest struct {
	ID        int    `json:"id"`
	Iid       int    `json:"iid"`
	ProjectID int    `json:"project_id"`
	Title     string `json:"title"`
	State     string `json:"state"`
	WebURL    string `json:"web_url"`
}

// storage — потокобезопасное хранилище известных ID MR с персистом в файл.
type storage struct {
	sync.RWMutex
	mrs    map[int]bool
	dbFile string
}

// Listen запускает цикл мониторинга. Конфигурация берётся из окружения.
func Listen(ctx context.Context) error {
	pollInterval, err := time.ParseDuration(os.Getenv("POLL_INTERVAL"))
	if err != nil || pollInterval <= 0 {
		pollInterval = 30 * time.Second
	}

	s, err := newStorage(defaultEnv("DB_FILENAME", "discovered_mrs.txt"))
	if err != nil {
		return err
	}

	token := os.Getenv("GITLAB_TOKEN")
	gitlabURL := strings.TrimSuffix(os.Getenv("GITLAB_URL"), "/")
	projectIDs := splitIDs(os.Getenv("GITLAB_PROJECTS"))
	if len(projectIDs) == 0 {
		return fmt.Errorf("GITLAB_PROJECTS не задан (список ID проектов через запятую)")
	}

	fmt.Printf("Загружена база известных MR: %d\n", s.len())

	// Сканируем один раз сразу, затем по тикеру.
	fmt.Println("Первичное сканирование проектов...")
	for _, id := range projectIDs {
		checkProject(ctx, s, id, gitlabURL, token)
	}

	fmt.Printf("Мониторинг запущен. Интервал: %v. Ожидание новых MR...\n", pollInterval)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			var wg sync.WaitGroup
			for _, id := range projectIDs {
				wg.Add(1)
				go func(id string) {
					defer wg.Done()
					checkProject(ctx, s, id, gitlabURL, token)
				}(id)
			}
			wg.Wait()
		}
	}
}

func newStorage(dbFile string) (*storage, error) {
	s := &storage{mrs: map[int]bool{}, dbFile: dbFile}
	if err := s.load(); err != nil {
		return nil, fmt.Errorf("загрузка базы известных MR %q: %w", dbFile, err)
	}
	return s, nil
}

func (s *storage) load() error {
	file, err := os.OpenFile(s.dbFile, os.O_RDONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	s.Lock()
	defer s.Unlock()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if id, err := strconv.Atoi(strings.TrimSpace(scanner.Text())); err == nil {
			s.mrs[id] = true
		}
	}
	return scanner.Err()
}

func (s *storage) len() int {
	s.RLock()
	defer s.RUnlock()
	return len(s.mrs)
}

// add запоминает MR и возвращает true, если он был новым.
func (s *storage) add(id int) (bool, error) {
	s.Lock()
	defer s.Unlock()

	if s.mrs[id] {
		return false, nil
	}

	file, err := os.OpenFile(s.dbFile, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return false, err
	}
	defer file.Close()

	if _, err := fmt.Fprintf(file, "%d\n", id); err != nil {
		return false, err
	}

	s.mrs[id] = true
	return true, nil
}

// checkProject опрашивает все страницы открытых MR проекта и сообщает о новых.
func checkProject(ctx context.Context, s *storage, projectID, gitlabURL, token string) {
	client := &http.Client{Timeout: 15 * time.Second}

	for page := 1; ; page++ {
		url := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests?state=opened&per_page=%d&page=%d",
			gitlabURL, projectID, pageSize, page)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			fmt.Printf("Ошибка создания запроса для проекта %s: %v\n", projectID, err)
			return
		}
		req.Header.Set("PRIVATE-TOKEN", token)

		resp, err := client.Do(req)
		if err != nil {
			fmt.Printf("Ошибка запроса для проекта %s: %v\n", projectID, err)
			return
		}

		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			fmt.Printf("Ошибка чтения ответа для проекта %s: %v\n", projectID, readErr)
			return
		}
		if resp.StatusCode != http.StatusOK {
			fmt.Printf("GitLab вернул ошибку для проекта %s: %s\n", projectID, resp.Status)
			return
		}

		var mrs []MergeRequest
		if err := json.Unmarshal(body, &mrs); err != nil {
			fmt.Printf("Ошибка парсинга JSON для проекта %s: %v\n", projectID, err)
			return
		}

		for _, mr := range mrs {
			isNew, err := s.add(mr.ID)
			if err != nil {
				fmt.Printf("Ошибка сохранения MR %d: %v\n", mr.ID, err)
				continue
			}
			if isNew {
				fmt.Printf("[%s] Внимание! Новый MR в проекте %d!\n", time.Now().Format("15:04:05"), mr.ProjectID)
				fmt.Printf("Название: %s\n", mr.Title)
				fmt.Printf("Ссылка: %s\n", mr.WebURL)
				fmt.Println("------------------------------------")
			}
		}

		// Если вернулось меньше страницы — это последняя; иначе есть следующая.
		if len(mrs) < pageSize {
			return
		}
	}
}

func splitIDs(raw string) []string {
	var out []string
	for _, v := range strings.Split(raw, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// defaultEnv возвращает переменную окружения или дефолт при пустом значении.
func defaultEnv(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
