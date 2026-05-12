#!/bin/bash

set -e

CONFIG_FILE="${1:-loadbalancer.json}"

echo "Generating docker-compose.yml from ${CONFIG_FILE}..."
go run generate-compose.go "${CONFIG_FILE}"

echo "Starting load balancer services..."

if ! docker info > /dev/null 2>&1; then
    echo "Error: Docker is not running"
    exit 1
fi

docker compose up --build -d

echo ""
echo "Services started successfully!"
echo ""

# Dynamically list server count from the config
SERVER_COUNT=$(grep -c '"id"' "${CONFIG_FILE}" || echo "?")
echo "Backend servers: ${SERVER_COUNT}"
echo ""
echo "Available endpoints:"
echo "  Prequal:       http://localhost:8080"
echo "  Round-Robin:   http://localhost:8081"
echo "  Prometheus:    http://localhost:9090"
echo "  Grafana:       http://localhost:3001 (admin/admin)"
echo ""
echo "Run './compare.sh' to test both algorithms side-by-side"
echo "Run 'docker compose logs -f' to view logs"
