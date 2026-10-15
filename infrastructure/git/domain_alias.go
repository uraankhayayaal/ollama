package git

import "ai/domain"

// Этот файл переводит git-понятия, вынесенные в domain (Этап 8, срез H3a), в
// алиасы пакета git: реализация git и outer-слой (server/tools) продолжают
// пользоваться привычными git.MergeResult/git.SanitizeBranchName и т.п., а
// application зависит только от domain.

type MergeResult = domain.MergeResult
type MergeConflictError = domain.MergeConflictError
type MergeFeatureOptions = domain.MergeFeatureOptions

const TaskBranchPrefix = domain.TaskBranchPrefix

var SanitizeBranchName = domain.SanitizeBranchName
var RedactSecrets = domain.RedactSecrets
var ErrWorkflowScope = domain.ErrWorkflowScope
