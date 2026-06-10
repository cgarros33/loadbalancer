package main

import (
	"flag"
	"fmt"
	"os"
	"text/template"
)

const composeTmpl = `services:
  loadbalancer-prequal:
    build: .
    container_name: lb-prequal
    ports:
      - "8080:8080"
    networks:
      - loadbalancer-net
    environment:
{{- range $i := .BackendRange}}
      - BACKEND_SERVER{{inc $i}}=server{{inc $i}}:80
{{- end}}
      - LB_ALGORITHM=prequal
      - QRIF={{.QRIF}}
      - PROBE_POOL_SIZE={{.ProbePoolSize}}
      - PROBE_RATE_MULTIPLIER={{printf "%.1f" .ProbeRateMultiplier}}
    depends_on:
{{- range $i := .BackendRange}}
      - server{{inc $i}}
{{- end}}
    ulimits:
      nofile:
        soft: 65536
        hard: 65536

  loadbalancer-rr:
    build: .
    container_name: lb-roundrobin
    ports:
      - "8081:8080"
    networks:
      - loadbalancer-net
    environment:
{{- range $i := .BackendRange}}
      - BACKEND_SERVER{{inc $i}}=server{{inc $i}}:80
{{- end}}
      - LB_ALGORITHM=roundrobin
      - QRIF={{.QRIF}}
      - PROBE_POOL_SIZE={{.ProbePoolSize}}
      - PROBE_RATE_MULTIPLIER={{printf "%.1f" .ProbeRateMultiplier}}
    depends_on:
{{- range $i := .BackendRange}}
      - server{{inc $i}}
{{- end}}
    ulimits:
      nofile:
        soft: 65536
        hard: 65536
{{range $i := .BackendRange}}
  server{{inc $i}}:
    build: ./backend
    container_name: server{{inc $i}}
    networks:
      - loadbalancer-net
    environment:
      - SERVER_ID=server{{inc $i}}
      - PORT=80
      - ANTAGONIST_BIAS={{index $.AntagonistBias $i}}
      - MEAN_DWELL_MS={{$.MeanDwellMS}}
      - BASE_SERVICE_MS={{$.BaseServiceMS}}
      - CAPACITY={{$.Capacity}}
      - DEBUG={{$.Debug}}
    cpus: {{$.CPUs}}
{{end}}
  prometheus:
    image: prom/prometheus
    ports:
      - "9090:9090"
    volumes:
      - ./config/prometheus/prometheus.yml:/etc/prometheus/prometheus.yml
    networks:
      - loadbalancer-net

  grafana:
    image: grafana/grafana
    ports:
      - "3001:3000"
    environment:
      - GF_SECURITY_ADMIN_PASSWORD=admin
      - GF_SECURITY_ADMIN_USER=admin
    volumes:
      - ./config/grafana/provisioning:/etc/grafana/provisioning
      - ./config/grafana/dashboards:/var/lib/grafana/dashboards
    networks:
      - loadbalancer-net
    depends_on:
      - prometheus

networks:
  loadbalancer-net:
    driver: bridge
`

type tmplData struct {
	BackendRange        []int
	AntagonistBias      []float64 // per-backend ANTAGONIST_BIAS (p_up), len == len(BackendRange)
	MeanDwellMS         int
	BaseServiceMS       int
	Capacity            int
	CPUs                string
	QRIF                float64
	ProbePoolSize       int
	ProbeRateMultiplier float64
	Debug               int
}

func main() {
	n := flag.Int("n", 10, "number of backend servers")
	hotBias := flag.Float64("hot-bias", 0.65, "ANTAGONIST_BIAS (p_up) for hot-prone backends")
	neutralBias := flag.Float64("neutral-bias", 0.5, "ANTAGONIST_BIAS (p_up) for neutral backends")
	coldBias := flag.Float64("cold-bias", 0.3, "ANTAGONIST_BIAS (p_up) for cold-prone backends")
	meanDwellMS := flag.Int("mean-dwell-ms", 2000, "MEAN_DWELL_MS for the antagonist random walk")
	baseMS := flag.Int("base-service-ms", 5, "BASE_SERVICE_MS baseline latency per request")
	capacity := flag.Int("capacity", 20, "CAPACITY per backend (processor-sharing capacity)")
	cpus := flag.Float64("cpus", 1.0, "Docker CPU quota per backend container")
	qrif := flag.Float64("qrif", 0.84, "QRIF quantile threshold for HCL selection")
	poolSize := flag.Int("probe-pool-size", 8, "PROBE_POOL_SIZE")
	rateMultiplier := flag.Float64("probe-rate-multiplier", 3.0, "PROBE_RATE_MULTIPLIER probes per request")
	debug := flag.Int("debug", 0, "set DEBUG=1 on all backends to log per-request detail")
	output := flag.String("output", "docker-compose.yml", "output file path (- for stdout)")
	flag.Parse()

	if *n < 1 {
		fmt.Fprintln(os.Stderr, "error: -n must be >= 1")
		os.Exit(1)
	}

	backendRange := make([]int, *n)
	for i := range backendRange {
		backendRange[i] = i
	}

	// Split backends into three antagonist-propensity groups: hot-prone,
	// neutral, cold-prone (roughly thirds). With the default biases
	// (0.65/0.5/0.3) the per-backend stationary mean antagonist levels are
	// ~66%/50%/~30%, averaging well under the 70% no-collapse threshold.
	nHot := *n / 3
	nCold := *n / 3
	nNeutral := *n - nHot - nCold
	biases := make([]float64, *n)
	for i := range biases {
		switch {
		case i < nHot:
			biases[i] = *hotBias
		case i < nHot+nNeutral:
			biases[i] = *neutralBias
		default:
			biases[i] = *coldBias
		}
	}

	data := tmplData{
		BackendRange:        backendRange,
		AntagonistBias:      biases,
		MeanDwellMS:         *meanDwellMS,
		BaseServiceMS:       *baseMS,
		Capacity:            *capacity,
		CPUs:                fmt.Sprintf("%.1f", *cpus),
		QRIF:                *qrif,
		ProbePoolSize:       *poolSize,
		ProbeRateMultiplier: *rateMultiplier,
		Debug:               *debug,
	}

	funcMap := template.FuncMap{
		"inc": func(i int) int { return i + 1 },
	}

	tmpl, err := template.New("compose").Funcs(funcMap).Parse(composeTmpl)
	if err != nil {
		fmt.Fprintf(os.Stderr, "template parse error: %v\n", err)
		os.Exit(1)
	}

	var w *os.File
	if *output == "-" {
		w = os.Stdout
	} else {
		w, err = os.Create(*output)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error creating %s: %v\n", *output, err)
			os.Exit(1)
		}
		defer w.Close()
	}

	if err := tmpl.Execute(w, data); err != nil {
		fmt.Fprintf(os.Stderr, "template execute error: %v\n", err)
		os.Exit(1)
	}

	if *output != "-" {
		fmt.Fprintf(os.Stderr, "generated %s: %d backends (%d hot-prone @ bias %.2f, %d neutral @ bias %.2f, %d cold-prone @ bias %.2f), mean-dwell-ms=%d, base-service-ms=%d, capacity=%d\n",
			*output, *n, nHot, *hotBias, nNeutral, *neutralBias, nCold, *coldBias, *meanDwellMS, *baseMS, *capacity)
	}

}
