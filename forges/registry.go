package forges

import "fmt"

// kind — идентификатор типа провайдера, по которому его можно построить.
type kind string

const (
	KindGitLab kind = "gitlab"
	KindGitHub kind = "github"
)

// Registry строителей провайдеров. Реализации регистрируют себя через
// Register при импорте (init), что позволяет добавлять новые хостинги
// без правки этого пакета.
var builders = map[kind]Builder{}

// Builder конструирует реализацию Forge по ссылке и токену.
type Builder func(prURL string, token string) (Forge, error)

// Register регистрирует строителя для типа провайдера.
func Register(k kind, b Builder) {
	builders[k] = b
}

// New создаёт провайдер по ссылке на Pull/Merge Request.
// Провайдер определяется по хостингу из URL; токен — для доступа к его API.
// Чтобы добавить новый хостинг, достаточно зарегистрировать строителя
// через Register и добавить хост в DetectType.
func New(prURL string, token string) (Forge, error) {
	kind := DetectType(prURL)
	if kind == "" {
		return nil, fmt.Errorf("не удалось определить тип хостинга по URL: %s", prURL)
	}

	builder, ok := builders[kind]
	if !ok {
		return nil, fmt.Errorf("провайдер %q не зарегистрирован", kind)
	}
	return builder(prURL, token)
}