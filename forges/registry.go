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

// Builder конструирует реализацию Forge по ссылке на существующий PR/MR.
type Builder func(prURL string, token string) (Forge, error)

// RemoteBuilder конструирует реализацию Forge по git-remote (без номера MR):
// используется, когда запрос на слияние ещё не создан, а нужно его открыть
// (Ф-2-3, HITL-затвор «Принять → MR»).
type RemoteBuilder func(remoteURL string, token string) (Forge, error)

// Register регистрирует строителя провайдера по ссылке на PR/MR.
func Register(k kind, b Builder) {
	builders[k] = b
}

// remoteBuilders — строители по git-remote.
var remoteBuilders = map[kind]RemoteBuilder{}

// RegisterRemote регистрирует строителя провайдера по git-remote.
func RegisterRemote(k kind, b RemoteBuilder) {
	remoteBuilders[k] = b
}

// New создаёт провайдер по ссылке на Pull/Merge Request.
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

// NewByRemote создаёт провайдер по git-remote (без номера PR/MR):
// используется HITL-затвором «Принять → MR» (Ф-2-3), когда фича-ветка уже
// запушена в remote, а сам запрос на слияние предстоит открыть
// (CreateMergeRequest).
func NewByRemote(remoteURL string, token string) (Forge, error) {
	kind := DetectType(remoteURL)
	if kind == "" {
		return nil, fmt.Errorf("не удалось определить тип хостинга по git-remote: %s", remoteURL)
	}

	builder, ok := remoteBuilders[kind]
	if !ok {
		return nil, fmt.Errorf("провайдер %q не зарегистрирован (по remote)", kind)
	}
	return builder(remoteURL, token)
}
