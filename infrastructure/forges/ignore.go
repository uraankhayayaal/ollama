package forges

import "ai/domain"

// IsIgnoredDir делегирует domain.IsIgnoredDir (Этап 8, срез H3b): правило
// общих служебных каталогов переехало в домен, чтобы application-слой не
// зависел от forges.
var IsIgnoredDir = domain.IsIgnoredDir
