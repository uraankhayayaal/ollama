// Разбор unified-диффа одного файла (patch из /api/projects/<name>/diff?file=)
// в side-by-side представление в стиле JetBrains: слева «как было», справа
// «как стало», добавленные строки — правый столбец (зелёный), удалённые —
// левый (красный).

export interface SideLine {
  no: number;          // номер строки в файле
  text: string;        // содержимое без префикса +/-
}

export interface SideRow {
  left: (SideLine & { kind: "del" | "ctx" }) | null;
  right: (SideLine & { kind: "add" | "ctx" }) | null;
}

export interface SideBySide {
  oldPath: string | null; // путь в версии «было» (a/...)
  newPath: string | null; // путь в версии «стало» (b/...)
  rows: SideRow[];
  binary: boolean;
}

// Разбор строки пути из +++/--- заголовка (срезаем a/, b/ и git-кавычки).
const diffPath = (raw: string): string => {
  let p = raw.trim();
  if (p.startsWith("a/")) p = p.slice(2);
  else if (p.startsWith("b/")) p = p.slice(2);
  if (p.startsWith('"')) {
    try {
      return JSON.parse(p) as string;
    } catch {
      /* продолжаем с сырой строкой */
    }
  }
  return p;
};

export function sideBySide(patch: string): SideBySide {
  const lines = patch.split(/\r?\n/);
  const res: SideBySide = { oldPath: null, newPath: null, rows: [], binary: false };

  let i = 0;
  while (i < lines.length) {
    const ln = lines[i];
    if (!ln) {
      i++;
      continue;
    }
    if (ln.startsWith("--- ")) {
      res.oldPath = diffPath(ln.slice(4));
      i++;
      continue;
    }
    if (ln.startsWith("+++ ")) {
      res.newPath = diffPath(ln.slice(4));
      i++;
      continue;
    }
    if (ln.startsWith("Binary files") || ln.includes("GIT binary patch")) {
      res.binary = true;
      i++;
      continue;
    }
    // Заголовок хунка: @@ -old,count +new,count @@ [контекст]
    const hunk = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(ln);
    if (hunk) {
      let oldNo = parseInt(hunk[1] ?? "1", 10);
      let newNo = parseInt(hunk[2] ?? "1", 10);
      i++;
      const block: string[] = [];
      while (i < lines.length) {
        const bl = lines[i];
        if (bl && /^@@ -/.test(bl)) break;
        block.push(bl ?? "");
        i++;
      }
      res.rows.push(...alignBlock(block, () => oldNo++, () => newNo++));
      continue;
    }
    i++;
  }
  return res;
}

// Выравнивание строк хунка в пары «старое ↔ новое». Удалённые строки идут в
// левый столбец, добавленные — в правый; соседние блоки -/+ сопоставляются
// попарно как «замена» (обе колонки на одной строке).
function alignBlock(
  block: string[],
  nextOld: () => number,
  nextNew: () => number,
): SideRow[] {
  const rows: SideRow[] = [];
  let i = 0;

  // Собрать подряд идущие строки одного префикса (+ или -).
  const take = (pre: "+" | "-"): string[] => {
    const out: string[] = [];
    while (i < block.length) {
      const ln = block[i];
      if (!ln || ln.charAt(0) !== pre) break;
      out.push(ln.slice(1));
      i++;
    }
    return out;
  };

  while (i < block.length) {
    const ln = block[i];
    if (!ln) {
      i++;
      continue;
    }
    const c = ln.charAt(0);
    if (c === " ") {
      rows.push({
        left: { no: nextOld(), text: ln.slice(1), kind: "ctx" },
        right: { no: nextNew(), text: ln.slice(1), kind: "ctx" },
      });
      i++;
      continue;
    }
    if (c === "\\") {
      // "\ No newline at end of file" — мета-строка, пропускаем.
      i++;
      continue;
    }
    if (c === "-" || c === "+") {
      const dels = take("-");
      const adds = take("+");
      const n = Math.max(dels.length, adds.length);
      for (let k = 0; k < n; k++) {
        const left =
          k < dels.length
            ? { no: nextOld(), text: dels[k] ?? "", kind: "del" as const }
            : null;
        const right =
          k < adds.length
            ? { no: nextNew(), text: adds[k] ?? "", kind: "add" as const }
            : null;
        rows.push({ left, right });
      }
      continue;
    }
    i++;
  }
  return rows;
}