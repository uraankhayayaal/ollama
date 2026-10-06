// Command sandbox-proxy — egress-прокси с белым списком для сессионной
// песочницы.
//
// Исполняется ВНУТРИ gateway-контейнера (Docker Desktop): контейнер сидит
// одновременно в изолированной внутренней сети сессий и в обычной сети с
// выходом в интернет, поэтому единственный способ достичь прокси из сессии —
// on-link адрес этого контейнера. Прокси пускает только домены из --allow,
// всё прочее — 403 с записью в лог.
//
// На нативном Linux прокси живёт в процессе сервера на шлюзе сети и этот
// бинарь не нужен (см. sandbox/egress.go).
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"ai/sandbox"
)

func main() {
	listen := flag.String("listen", ":3128", "адрес слушателя")
	allow := flag.String("allow", "", "белый список доменов через запятую (example.com,*.example.com)")
	flag.Parse()

	domains := splitAllow(*allow)
	if len(domains) == 0 {
		fmt.Fprintln(os.Stderr, "sandbox-proxy: --allow пуст — прокси без белого списка недопустим")
		os.Exit(2)
	}
	if err := sandbox.ServeEgressProxy(*listen, domains); err != nil {
		fmt.Fprintln(os.Stderr, "sandbox-proxy:", err)
		os.Exit(1)
	}
}

// splitAllow — CSV из флага → чистый список доменов.
func splitAllow(csv string) []string {
	var out []string
	for _, d := range strings.Split(csv, ",") {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	return out
}
