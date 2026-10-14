// Привязка git-репозитория к порту домена (Этап 4, срез E3).
//
// `infrastructure/git` — бывший пакет `gitops`, перенесённый под
// `infrastructure/`. Порт `domain/services.GitOperations` объявляет операции
// рабочей копии Git, которыми пользуется application-слой. `*Repo` реализует
// его напрямую (подписи методов совпадают), поэтому адаптер не нужен: связь
// зафиксирована compile-assertion ниже.
//
// Создание рабочей копии (Clone/Worktree/BranchInPlace) требует конкретного
// Executor — это инфраструктурная деталь, поэтому конструкторы остаются у
// пакета, а вызывающий слой получает уже готовый `*Repo` и работает с ним
// через порт. Если в будущем application-слою понадобится создавать копии
// самому, здесь появится фабричный порт.

package git

import "ai/domain/services"

// Compile-time проверка: репозиторий реализует порт `domain/services.GitOperations`.
var _ services.GitOperations = (*Repo)(nil)
