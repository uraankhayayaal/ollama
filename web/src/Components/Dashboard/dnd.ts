// Общее состояние drag&drop доски: идентификатор перетаскиваемой задачи.
// TaskCard выставляет его при onDragStart, Column читает на drop. Через эту
// крошечную обёртку компоненты остаются изолированными друг от друга и не
// зависят от глобальной переменной Dashboard.
let dragID: string | null = null;

export function getDragID(): string | null {
  return dragID;
}

export function setDragID(id: string | null): void {
  dragID = id;
}

export function clearDragID(): void {
  dragID = null;
}