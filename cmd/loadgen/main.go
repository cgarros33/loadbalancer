package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/omarshaarawi/loadbalancer/internal/loadgen"
)

func main() {
	url := flag.String("url", "http://localhost:8080/", "target URL")
	qps := flag.Float64("qps", 100, "requests per second")
	dur := flag.Duration("duration", 30*time.Second, "duration")
	flag.Parse()

	res := loadgen.Run(context.Background(), loadgen.Config{
		URL:      *url,
		QPS:      *qps,
		Duration: *dur,
	})

	errPct := float64(0)
	if res.Total > 0 {
		errPct = float64(res.Errors) / float64(res.Total) * 100
	}
	fmt.Printf("total=%-6d errors=%-6d (%.1f%%)  p50=%-8s p99=%-8s p999=%s\n",
		res.Total, res.Errors, errPct, res.P50, res.P99, res.P999)
}
