package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joho/godotenv"
)

type MergeRequest struct {
	ID        int    `json:"id"`
	Iid       int    `json:"iid"`
	ProjectID int    `json:"project_id"`
	Title     string `json:"title"`
	State     string `json:"state"`
	WebURL    string `json:"web_url"`
}

// Потокобезопасное хранилище для ID MR
type MRStorage struct {
	sync.RWMutex
	mrs map[int]bool
}

var storage = &MRStorage{mrs: make(map[int]bool)}

// Загрузка ID из файла при старте
func loadDiscoveredMRs(dbFileName string) error {
	file, err := os.OpenFile(dbFileName, os.O_RDONLY|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	storage.Lock()
	defer storage.Unlock()

	for scanner.Scan() {
		line := scanner.Text()
		if id, err := strconv.Atoi(line); err == nil {
			storage.mrs[id] = true
		}
	}
	return scanner.Err()
}

// Сохранение нового ID в файл и память
func saveNewMR(dbFileName string, id int) error {
	storage.Lock()
	defer storage.Unlock()

	// Если уже есть в памяти, ничего не делаем
	if storage.mrs[id] {
		return nil
	}

	// Открываем файл в режиме добавления (Append)
	file, err := os.OpenFile(dbFileName, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	if _, err := fmt.Fprintf(file, "%d\n", id); err != nil {
		return err
	}

	storage.mrs[id] = true
	return nil
}

func checkProjectMRs(projectID, gitlabURL, dbFileName, accessToken string) {
	url := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests?state=opened", gitlabURL, projectID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		fmt.Printf("Ошибка создания запроса для проекта %s: %v\n", projectID, err)
		return
	}

	req.Header.Set("PRIVATE-TOKEN", accessToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("Ошибка выполнения запроса для проекта %s: %v\n", projectID, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Printf("GitLab вернул ошибку для проекта %s: %s\n", projectID, resp.Status)
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Printf("Ошибка чтения ответа для проекта %s: %v\n", projectID, err)
		return
	}

	var mrs []MergeRequest
	if err := json.Unmarshal(body, &mrs); err != nil {
		fmt.Printf("Ошибка парсинга JSON для проекта %s: %v\n", projectID, err)
		return
	}

	for _, mr := range mrs {
		// Проверяем наличие в памяти (быстрое чтение)
		storage.RLock()
		known := storage.mrs[mr.ID]
		storage.RUnlock()

		if !known {
			// Сохраняем в файл и память
			if err := saveNewMR(dbFileName, mr.ID); err != nil {
				fmt.Printf("Ошибка сохранения MR %d в базу: %v\n", mr.ID, err)
				continue
			}

			// Выводим оповещение
			fmt.Printf("[%s] Внимание! Новый MR в проекте %d!\n", time.Now().Format("15:04:05"), mr.ProjectID)
			fmt.Printf("Название: %s\n", mr.Title)
			fmt.Printf("Ссылка: %s\n", mr.WebURL)
			fmt.Println("------------------------------------")

			// @TODO: Make code review
		}
	}
}

func main() {
	// Load the .env file
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	// Получаем строку из переменной окружения
	rawTimeout := os.Getenv("POLL_INTERVAL")
	if rawTimeout == "" {
		rawTimeout = "30s" // Дефолтное значение, если .env не задан
	}

	// Парсим строку в тип time.Duration
	pollInterval, err := time.ParseDuration(rawTimeout)
	if err != nil {
		fmt.Println("Ошибка парсинга:", err)
		return
	}

	dbFileName := os.Getenv("DB_FILENAME")
	accessToken := os.Getenv("GITLAB_TOKEN")
	gitlabURL := os.Getenv("GITLAB_URL")
	rawProjectIDs := os.Getenv("GITLAB_PROJECTS")

	// Список ID ваших проектов в GitLab
	var projectIDs = strings.Split(rawProjectIDs, ",")

	// 1. Инициализируем базу данных из файла
	fmt.Println("Загрузка базы данных известных MR...")
	if err := loadDiscoveredMRs(dbFileName); err != nil {
		log.Fatalf("Критическая ошибка загрузки базы данных: %v", err)
	}

	storage.RLock()
	knownCount := len(storage.mrs)
	storage.RUnlock()
	fmt.Printf("База загружена. Известно MR: %d\n", knownCount)

	// 2. Первый запуск: сканируем текущие MR.
	// Если это самый первый запуск программы (файл пустой), он молча внесет все текущие MR в базу, чтобы не спамить.
	// При последующих перезапусках старые MR просто проигнорируются.
	fmt.Println("Сканирование проектов...")
	for _, id := range projectIDs {
		checkProjectMRs(id, gitlabURL, dbFileName, accessToken)
	}

	fmt.Printf("Мониторинг запущен. Интервал: %v. Ожидание новых MR...\n", pollInterval)

	// 3. Основной цикл опроса
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for range ticker.C {
		for _, id := range projectIDs {
			go checkProjectMRs(id, gitlabURL, dbFileName, accessToken) // Проверка каждого проекта параллельно
		}
	}
}
