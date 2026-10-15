// Package all — точка регистрации всех встроенных провайдеров систем ревью.
//
// Чтобы фабрика forges.New могла создавать реализации, пакеты-провайдеры
// должны быть импортированы (их init() регистрирует себя в реестре).
// Достаточно один раз (blank) импортировать этот пакет там, где нужна
// фабрика, например в main:
//
//	import _ "ai/infrastructure/forges/all"
//	...
//	forge, err := forges.New(url, token)
package all

import (
	_ "ai/infrastructure/forges/github"
	_ "ai/infrastructure/forges/gitlab"
)
