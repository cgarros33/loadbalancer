package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/omarshaarawi/loadbalancer/internal/loadgen"
)

func main() {
	targetURL := flag.String("url", "http://localhost:8080/", "target URL")
	qps := flag.Float64("qps", 100, "requests per second")
	dur := flag.Duration("duration", 30*time.Second, "duration")
	jsonOut := flag.Bool("json", false, "print result as JSON (for programmatic consumers)")
	flag.Parse()

	warnIfLoopback(*targetURL)

	res := loadgen.Run(context.Background(), loadgen.Config{
		URL:      *targetURL,
		QPS:      *qps,
		Duration: *dur,
	})

	if *jsonOut {
		b, err := json.Marshal(res)
		if err != nil {
			fmt.Println("{}")
			return
		}
		fmt.Println(string(b))
		return
	}

	errPct := float64(0)
	if res.Total > 0 {
		errPct = float64(res.Errors) / float64(res.Total) * 100
	}
	fmt.Printf("total=%-6d errors=%-6d (%.1f%%)  p50=%-8s p99=%-8s p999=%s\n",
		res.Total, res.Errors, errPct, res.P50, res.P99, res.P999)
}

// warnIfLoopback prints a warning when targetURL points at localhost/127.0.0.1,
// i.e. a Docker-published port reached from the host. On Docker Desktop/WSL2
// this traffic crosses a slow VM-to-VM relay that can itself become the
// bottleneck under sustained load, producing error rates that do not reflect
// the load balancer's real behavior. Prefer running loadgen as a container on
// the compose network instead, e.g.:
//
//	docker compose --profile tools run --rm -T loadgen \
//	    -url http://lb-prequal:8080 -qps 5000 -duration 30s -json
func warnIfLoopback(targetURL string) {
	u, err := url.Parse(targetURL)
	if err != nil {
		return
	}
	host := u.Hostname()
	if host == "localhost" || host == "127.0.0.1" || host == "::1" || strings.HasPrefix(host, "127.") {
		fmt.Fprintln(os.Stderr, "WARNING: target host is "+host+" — if this is a Docker-published port,")
		fmt.Fprintln(os.Stderr, "         traffic from the host goes through a slow Docker Desktop/WSL2 relay")
		fmt.Fprintln(os.Stderr, "         that can become the bottleneck and produce misleading error rates.")
		fmt.Fprintln(os.Stderr, "         Prefer running loadgen as a container on the compose network instead:")
		fmt.Fprintln(os.Stderr, "           docker compose --profile tools run --rm -T loadgen \\")
		fmt.Fprintln(os.Stderr, "               -url http://lb-prequal:8080 -qps <qps> -duration <dur> -json")
	}
}
