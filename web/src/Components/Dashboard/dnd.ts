// Общее состояние drag&drop доски. TaskCard выставляет идентификатор
// перетаскиваемой задачи при onDragStart, Cell читает его на drop. EpicRow
// аналогично держит ID перетаскиваемого эпика для пересортировки строк. Через
// эту крошечную обёртку компоненты остаются изолированными друг от друга и не
// зависят от глобальной переменной Dashboard.
let dragID: string | null = null;
let epicDragID: string | null = null;

export function getDragID(): string | null {
  return dragID;
}

export function setDragID(id: string | null): void {
  dragID = id;
}

export function clearDragID(): void {
  dragID = null;
}

export function getEpicDragID(): string | null {
  return epicDragID;
}

export function setEpicDragID(id: string | null): void {
  epicDragID = id;
}

export function clearEpicDragID(): void {
  epicDragID = null;
}