package git

import (
	"ai/domain"
	"ai/domain/services"
)

// Этот файл переводит git-понятия, вынесенные в domain/services (Этап 8, срезы
// H3a/H3b), в алиасы пакета git: реализация git и outer-слой (server/tools)
// продолжают пользоваться привычными git.MergeResult/git.Executor и т.п., а
// application зависит только от domain/services.

type MergeResult = domain.MergeResult
type MergeConflictError = domain.MergeConflictError
type MergeFeatureOptions = domain.MergeFeatureOptions
type Executor = services.Executor

const TaskBranchPrefix = domain.TaskBranchPrefix

var SanitizeBranchName = domain.SanitizeBranchName
var RedactSecrets = domain.RedactSecrets
var ErrWorkflowScope = domain.ErrWorkflowScope
