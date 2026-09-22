// Адаптер LSP-оглавлений для расширенного сжатия (Ф-9).
//
// OutlineFromOutliner строит функцию оглавлений из LSP Outliner: по списку
// вытесняемых файлов запрашивает documentSymbol и склеивает текстовые
// оглавления (имя/тип : строка). Не читает код файлов и не грузит содержимое
// в памятку — модель получает «карту» файла, а детали ищет инструментами.

package tools

import (
	"context"
	"fmt"
	"strings"

	"ai/tools/lspclient"
)

// maxOutlineFiles — сколько файлов максимально включается в одну выдачу
// оглавлений (лимит объёма памятки).
const maxOutlineFiles = 4

// OutlineFromOutliner возвращает функцию оглавлений поверх LSP Outliner:
// signature совпадает с OutlineFn сжатия (CompressionClient.Outline).
// Файлы-одиночки без синтаксиса (нет сервера/символов) пропускаются; если
// ничего не набрано вовсе — возвращается ошибка (сжатие деградирует молча).
func OutlineFromOutliner(o lspclient.Outliner) func(ctx context.Context, project string, rels []string) (string, error) {
	return func(ctx context.Context, project string, rels []string) (string, error) {
		if len(rels) == 0 {
			return "", nil
		}
		if len(rels) > maxOutlineFiles {
			rels = rels[:maxOutlineFiles]
		}
		var b strings.Builder
		skipped := 0
		for _, rel := range rels {
			syms, err := o.DocumentSymbols(ctx, rel)
			if err != nil || len(syms) == 0 {
				skipped++
				continue
			}
			txt := lspclient.FormatOutline(rel, syms)
			if strings.TrimSpace(txt) == "" {
				skipped++
				continue
			}
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(txt)
		}
		if b.Len() == 0 {
			return "", fmt.Errorf("нет оглавлений для %d файлов (пропущено %d)", len(rels), skipped)
		}
		return b.String(), nil
	}
}