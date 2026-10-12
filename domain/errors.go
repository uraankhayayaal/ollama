package domain

import "errors"

// ErrNotFound — сущность доски или метаданные не найдены.
var ErrNotFound = errors.New("запись на доске не найдена")

// ErrExists — запись с таким ID уже есть на доске.
var ErrExists = errors.New("запись с таким ID уже есть на доске")
